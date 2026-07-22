package projection

import "testing"

// AnchorFromKey is BuildKey's inverse and the convergence sweep's ownership
// test. It has to round-trip exactly — a key the sweep cannot invert reads as
// an orphan row to retract, and a key it inverts too eagerly is a sibling
// lens's row it would retract instead.

func TestAnchorFromKey_RoundTripsBuildKey(t *testing.T) {
	cases := []struct {
		name  string
		desc  OutputDescriptor
		actor string
	}{
		{
			name:  "default suffix",
			desc:  OutputDescriptor{AnchorType: "identity", OutputKeyPattern: "cap.roles.{actorSuffix}"},
			actor: "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
		},
		{
			name:  "primary capability key",
			desc:  OutputDescriptor{AnchorType: "identity", OutputKeyPattern: "cap.{actorSuffix}"},
			actor: "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
		},
		{
			name:  "bare NanoID suffix (keyColumn)",
			desc:  OutputDescriptor{AnchorType: "leaseapp", OutputKeyPattern: "leaseApplicationComplete.{actorSuffix}", KeyColumn: "entityId"},
			actor: "vtx.leaseapp.Lk2Pn6mQrtwzKbcXvP3T",
		},
		{
			name:  "literal suffix after the placeholder",
			desc:  OutputDescriptor{AnchorType: "identity", OutputKeyPattern: "cap.{actorSuffix}.grants"},
			actor: "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.desc.AnchorFromKey(tc.desc.BuildKey(tc.actor))
			if !ok {
				t.Fatalf("AnchorFromKey rejected this descriptor's own key %q", tc.desc.BuildKey(tc.actor))
			}
			if got != tc.actor {
				t.Fatalf("round trip: got %q, want %q", got, tc.actor)
			}
		})
	}
}

func TestAnchorFromKey_RejectsKeysThisLensDoesNotOwn(t *testing.T) {
	// capability-kv is shared by every auth-plane lens, so the ownership test
	// is what keeps one lens from retracting another's rows.
	primary := OutputDescriptor{AnchorType: "identity", OutputKeyPattern: "cap.{actorSuffix}"}
	roles := OutputDescriptor{AnchorType: "identity", OutputKeyPattern: "cap.roles.{actorSuffix}"}

	foreign := []struct {
		desc OutputDescriptor
		key  string
		why  string
	}{
		{primary, "cap.roles.identity.Hj4kPmRtw9nbCxz5vQ2y", "the roles lens's key under the primary's prefix"},
		{roles, "cap.identity.Hj4kPmRtw9nbCxz5vQ2y", "the primary lens's key"},
		{roles, "cap.role-by-operation.lattice.role.assign", "the operation-aggregate index"},
		{roles, "cap.roles.service.Hj4kPmRtw9nbCxz5vQ2y", "a different anchor type"},
		{roles, "cap.roles.identity.not-a-nanoid", "a malformed id segment"},
		{roles, "", "an empty key"},
	}
	for _, f := range foreign {
		if got, ok := f.desc.AnchorFromKey(f.key); ok {
			t.Fatalf("%s: AnchorFromKey(%q) claimed %q; a foreign key must be refused", f.why, f.key, got)
		}
	}
}

func TestAnchorFromKey_KeyColumnDescriptorRefusesANonNanoIDSuffix(t *testing.T) {
	// With keyColumn the type segment comes from the descriptor, so the suffix
	// is the only thing standing between a foreign key and a fabricated anchor.
	d := OutputDescriptor{AnchorType: "leaseapp", OutputKeyPattern: "leaseApplicationComplete.{actorSuffix}", KeyColumn: "entityId"}
	if _, ok := d.AnchorFromKey("leaseApplicationComplete.leaseapp.Lk2Pn6mQrtwzKbcXvP3T"); ok {
		t.Fatal("a <type>.<id> suffix must not parse under a bare-NanoID descriptor")
	}
}
