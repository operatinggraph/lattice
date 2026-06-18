package weaver

import "testing"

// splitRowKey is the frozen §10.2 weaver-targets key parser. These tests pin the
// contract Refractor's §10.2 Option (b) keyColumn mechanism builds to: a
// <targetId>.<entityId> key whose entity segment is a BARE NanoID round-trips,
// while the actorAggregate default <targetId>.<type>.<id> (the M2 defect) is
// rejected and dropped. The keys here are the exact shapes
// projection.OutputDescriptor.BuildKey emits with / without a keyColumn — proving
// the round-trip without any Weaver change.
func TestSplitRowKey_AcceptsKeyColumnProjectedKey(t *testing.T) {
	// The keyColumn-projected key: <targetId>.<bareNanoID> (one dot).
	targetID, entityID, ok := splitRowKey("leaseApplicationComplete.Lk2Pn6mQrtwzKbcXvP3T")
	if !ok {
		t.Fatalf("splitRowKey must accept a bare-NanoID key (the §10.2 Option b shape)")
	}
	if targetID != "leaseApplicationComplete" {
		t.Fatalf("targetID: got %q, want %q", targetID, "leaseApplicationComplete")
	}
	if entityID != "Lk2Pn6mQrtwzKbcXvP3T" {
		t.Fatalf("entityID: got %q, want bare NanoID", entityID)
	}
}

func TestSplitRowKey_RejectsDefaultTypeIDKey(t *testing.T) {
	// The actorAggregate default suffix: <targetId>.<type>.<id> (two dots after
	// the targetId). entityID becomes "leaseapp.Lk2Pn6mQrtwzKbcXvP3T", which is
	// not a bare NanoID, so the key is dropped — the M2 defect Option (b) fixes.
	if _, _, ok := splitRowKey("leaseApplicationComplete.leaseapp.Lk2Pn6mQrtwzKbcXvP3T"); ok {
		t.Fatalf("splitRowKey must reject a <type>.<id> entity segment (the M2 defect)")
	}
}
