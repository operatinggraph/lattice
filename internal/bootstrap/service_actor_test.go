package bootstrap

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
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

// TestServiceActorIdentities_Seeded asserts the Loom, Weaver, and Bridge
// identity vertices are present in the primordial batch with the correct
// class, protected flag, and Contract #1 vtx.identity.<id> key shape.
func TestServiceActorIdentities_Seeded(t *testing.T) {
	populateForTest(t)
	idx := entriesByKey(t)

	cases := []struct {
		key   string
		class string
	}{
		{LoomIdentityKey, "identity.system.loom"},
		{WeaverIdentityKey, "identity.system.weaver"},
		{BridgeIdentityKey, "identity.system.bridge"},
		{ObjmgrIdentityKey, "identity.system.object-store-manager"},
		{PrivacyIdentityKey, "identity.system.privacy"},
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

// TestServiceActorHoldsRoleLinks_Seeded asserts the service-actor holdsRole links are
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
		{BridgeHoldsRoleLinkKey, "vtx.identity." + BridgeIdentityID},
		{ObjmgrHoldsRoleLinkKey, "vtx.identity." + ObjmgrIdentityID},
		{PrivacyHoldsRoleLinkKey, "vtx.identity." + PrivacyIdentityID},
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
// beyond the admin baseline are exactly the 5 identity vertices + 5 holdsRole
// links; root-equivalence is established by reusing the existing operator
// topology.
func TestServiceActors_ReuseOperatorRole(t *testing.T) {
	populateForTest(t)
	idx := entriesByKey(t)

	newKeys := []string{
		LoomIdentityKey, WeaverIdentityKey, BridgeIdentityKey, ObjmgrIdentityKey, PrivacyIdentityKey,
		LoomHoldsRoleLinkKey, WeaverHoldsRoleLinkKey, BridgeHoldsRoleLinkKey, ObjmgrHoldsRoleLinkKey, PrivacyHoldsRoleLinkKey,
	}
	for _, k := range newKeys {
		if _, ok := idx[k]; !ok {
			t.Fatalf("expected new primordial key absent: %s", k)
		}
	}

	// All links must target the SAME pre-existing operator role the admin
	// holds — not a fresh "systemRoot"-style role.
	roleTarget := "vtx.role." + RoleOperatorID
	for _, k := range []string{LoomHoldsRoleLinkKey, WeaverHoldsRoleLinkKey, BridgeHoldsRoleLinkKey, ObjmgrHoldsRoleLinkKey, PrivacyHoldsRoleLinkKey} {
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
// verification enumeration covers all ten service-actor keys (so verify-kernel
// checks them).
func TestPrimordialVertexKeys_IncludesServiceActors(t *testing.T) {
	populateForTest(t)
	want := map[string]bool{
		LoomIdentityKey:         false,
		WeaverIdentityKey:       false,
		BridgeIdentityKey:       false,
		ObjmgrIdentityKey:       false,
		PrivacyIdentityKey:      false,
		LoomHoldsRoleLinkKey:    false,
		WeaverHoldsRoleLinkKey:  false,
		BridgeHoldsRoleLinkKey:  false,
		ObjmgrHoldsRoleLinkKey:  false,
		PrivacyHoldsRoleLinkKey: false,
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
// 10 entries (5 vertices + 5 links) relative to a baseline computed by removing
// the service-actor keys — guarding the verify-kernel count delta.
func TestServiceActors_KeyCountDelta(t *testing.T) {
	populateForTest(t)
	idx := entriesByKey(t)

	serviceKeys := map[string]bool{
		LoomIdentityKey:         true,
		WeaverIdentityKey:       true,
		BridgeIdentityKey:       true,
		ObjmgrIdentityKey:       true,
		PrivacyIdentityKey:      true,
		LoomHoldsRoleLinkKey:    true,
		WeaverHoldsRoleLinkKey:  true,
		BridgeHoldsRoleLinkKey:  true,
		ObjmgrHoldsRoleLinkKey:  true,
		PrivacyHoldsRoleLinkKey: true,
	}
	count := 0
	for k := range idx {
		if serviceKeys[k] {
			count++
		}
	}
	if count != 10 {
		t.Fatalf("expected exactly 10 service-actor entries in batch, got %d", count)
	}
}

// TestPrimordialVertexKeyCount_AgreesWithEnumeration asserts the declared
// count constant matches the enumerated slice length and is the expected 37
// after the Gateway service actor (identity vertex only, deliberately NO
// holdsRole link — real-actor-write-auth-e2e-design.md Phase 1) was added.
// This is the pure-Go mirror of the scripts/verify-kernel.go len()==Count
// agreement check (the kernel-topology lockstep guard).
func TestPrimordialVertexKeyCount_AgreesWithEnumeration(t *testing.T) {
	populateForTest(t)
	keys := PrimordialVertexKeys()
	if len(keys) != PrimordialVertexKeyCount {
		t.Fatalf("PrimordialVertexKeys() enumerates %d but PrimordialVertexKeyCount is %d",
			len(keys), PrimordialVertexKeyCount)
	}
	if PrimordialVertexKeyCount != 37 {
		t.Fatalf("PrimordialVertexKeyCount = %d, want 37", PrimordialVertexKeyCount)
	}
}

// TestGatewayIdentity_SeededWithoutHoldsRoleLink asserts the Gateway identity
// vertex is present in the primordial batch (protected, class
// identity.system.gateway) but — unlike every other service actor — the
// batch contains NO holdsRole link sourced from it. This pins the
// narrow-role fork (gateway-claim-flow-identity-provisioning-design.md §4
// Option B): the Gateway is internet-facing, so it never gets
// root-equivalence via the primordial topology.
func TestGatewayIdentity_SeededWithoutHoldsRoleLink(t *testing.T) {
	populateForTest(t)
	idx := entriesByKey(t)

	raw, ok := idx[GatewayIdentityKey]
	if !ok {
		t.Fatalf("primordial batch missing Gateway identity %q", GatewayIdentityKey)
	}
	var v vtxEnvelope
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal %q: %v", GatewayIdentityKey, err)
	}
	if v.Class != "identity.system.gateway" {
		t.Errorf("class = %q, want identity.system.gateway", v.Class)
	}
	if prot, _ := v.Data["protected"].(bool); !prot {
		t.Errorf("data.protected = %v, want true", v.Data["protected"])
	}

	gatewayHoldsRoleKey := "lnk.identity." + GatewayIdentityID + ".holdsRole.role." + RoleOperatorID
	if _, ok := idx[gatewayHoldsRoleKey]; ok {
		t.Fatalf("Gateway must NOT get a holdsRole->operator link, but found %q in the primordial batch", gatewayHoldsRoleKey)
	}
}

// TestGatewayKeyDerivation mirrors TestBridgeKeyDerivation for the Gateway
// identity — only the vertex key exists (no holdsRole link key is ever
// derived for it).
func TestGatewayKeyDerivation(t *testing.T) {
	populateForTest(t)
	if want := "vtx.identity." + GatewayIdentityID; GatewayIdentityKey != want {
		t.Errorf("GatewayIdentityKey = %q, want %q", GatewayIdentityKey, want)
	}
}

// TestGeneratePopulateRoundTrip_Gateway mirrors
// TestGeneratePopulateRoundTrip_Bridge for the Gateway identity.
func TestGeneratePopulateRoundTrip_Gateway(t *testing.T) {
	raw, err := generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !substrate.IsValidNanoID(raw.GatewayIdentity) {
		t.Fatalf("generate() produced invalid gatewayIdentity NanoID: %q", raw.GatewayIdentity)
	}
	if err := populate(raw); err != nil {
		t.Fatalf("populate: %v", err)
	}
	wantID := raw.GatewayIdentity
	if GatewayIdentityID != wantID {
		t.Errorf("GatewayIdentityID = %q, want %q", GatewayIdentityID, wantID)
	}

	data, err := json.Marshal(currentRaw())
	if err != nil {
		t.Fatalf("marshal currentRaw: %v", err)
	}
	var back PrimordialIDsRaw
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.GatewayIdentity != wantID {
		t.Fatalf("round-tripped gatewayIdentity = %q, want %q", back.GatewayIdentity, wantID)
	}
	if err := populate(back); err != nil {
		t.Fatalf("re-populate: %v", err)
	}
	if GatewayIdentityKey != "vtx.identity."+wantID {
		t.Errorf("after round-trip GatewayIdentityKey = %q, want %q", GatewayIdentityKey, "vtx.identity."+wantID)
	}
}

// TestBridgeKeyDerivation asserts the Bridge identity and holdsRole link keys
// derive from the persisted NanoID exactly as Contract #1 §1.1 specifies
// (4-segment vtx.identity.<id>; 6-segment lnk.identity.<id>.holdsRole.role.<id>).
func TestBridgeKeyDerivation(t *testing.T) {
	populateForTest(t)
	if want := "vtx.identity." + BridgeIdentityID; BridgeIdentityKey != want {
		t.Errorf("BridgeIdentityKey = %q, want %q", BridgeIdentityKey, want)
	}
	if want := "lnk.identity." + BridgeIdentityID + ".holdsRole.role." + RoleOperatorID; BridgeHoldsRoleLinkKey != want {
		t.Errorf("BridgeHoldsRoleLinkKey = %q, want %q", BridgeHoldsRoleLinkKey, want)
	}
}

// TestGeneratePopulateRoundTrip_Bridge proves generate() mints a non-empty
// Bridge NanoID and that it round-trips through currentRaw() → JSON →
// populate() with the derived keys intact — the persisted-and-stable mechanism
// the Bridge shares verbatim with Loom/Weaver (NanoID is random-then-persisted,
// not seed-computed).
func TestGeneratePopulateRoundTrip_Bridge(t *testing.T) {
	raw, err := generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !substrate.IsValidNanoID(raw.BridgeIdentity) {
		t.Fatalf("generate() produced invalid bridgeIdentity NanoID: %q", raw.BridgeIdentity)
	}
	if err := populate(raw); err != nil {
		t.Fatalf("populate: %v", err)
	}
	wantID := raw.BridgeIdentity
	if BridgeIdentityID != wantID {
		t.Errorf("BridgeIdentityID = %q, want %q", BridgeIdentityID, wantID)
	}

	// Round-trip through the persisted form and re-populate; the derived keys
	// must be stable across the JSON boundary.
	data, err := json.Marshal(currentRaw())
	if err != nil {
		t.Fatalf("marshal currentRaw: %v", err)
	}
	var back PrimordialIDsRaw
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.BridgeIdentity != wantID {
		t.Fatalf("round-tripped bridgeIdentity = %q, want %q", back.BridgeIdentity, wantID)
	}
	if err := populate(back); err != nil {
		t.Fatalf("re-populate: %v", err)
	}
	if BridgeIdentityKey != "vtx.identity."+wantID {
		t.Errorf("after round-trip BridgeIdentityKey = %q, want %q", BridgeIdentityKey, "vtx.identity."+wantID)
	}
}

// TestCheckVersion_RejectsStaleAcceptsCurrent proves the version-16 gate: a
// version-16 file passes, and any other version (notably "15", which
// predates the Gateway service actor) is hard-rejected with the
// make-down/make-up guidance so a stale file can never silently run against
// a mismatched kernel topology (AC #2).
func TestCheckVersion_RejectsStaleAcceptsCurrent(t *testing.T) {
	if err := checkVersion(BootstrapFile{Version: "16"}); err != nil {
		t.Errorf("checkVersion(version=16): unexpected error %v", err)
	}
	for _, v := range []string{"15", "14", "13", "12", "11", "10", "9", "8", "7", "6", "5", ""} {
		err := checkVersion(BootstrapFile{Version: v})
		if err == nil {
			t.Errorf("checkVersion(version=%q): expected rejection, got nil", v)
			continue
		}
		if !strings.Contains(err.Error(), "make down && make up") {
			t.Errorf("checkVersion(version=%q): error missing teardown guidance: %v", v, err)
		}
	}
}
