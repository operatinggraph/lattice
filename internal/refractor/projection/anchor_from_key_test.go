package projection

import (
	"strings"
	"testing"
)

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

func TestKeyPrefix_AcceptsTheShippedPatternsAndRefusesTheUnscopableOnes(t *testing.T) {
	// The prefix becomes a NATS subject filter (prefix + ">"), so it has to end
	// on a segment boundary; and a pattern that starts at the actor suffix
	// scopes to the whole target, which is the thing scoping exists to avoid.
	accepted := map[string]string{
		"cap.{actorSuffix}":                      "cap.",
		"cap.roles.{actorSuffix}":                "cap.roles.",
		"unroutedTasks.{actorSuffix}":            "unroutedTasks.",
		"leaseApplicationComplete.{actorSuffix}": "leaseApplicationComplete.",
	}
	for pattern, want := range accepted {
		d := OutputDescriptor{AnchorType: "identity", OutputKeyPattern: pattern}
		got, ok := d.KeyPrefix()
		if !ok {
			t.Fatalf("pattern %q: expected a usable key prefix", pattern)
		}
		if got != want {
			t.Fatalf("pattern %q: key prefix = %q, want %q", pattern, got, want)
		}
	}

	refused := map[string]string{
		"{actorSuffix}":      "no literal prefix at all",
		"cap{actorSuffix}":   "a prefix that does not end on a segment boundary",
		"cap.roles.identity": "no actor-suffix placeholder, so no per-anchor keys",
	}
	for pattern, why := range refused {
		d := OutputDescriptor{AnchorType: "identity", OutputKeyPattern: pattern}
		if got, ok := d.KeyPrefix(); ok {
			t.Fatalf("%s: pattern %q returned prefix %q; it must be refused", why, pattern, got)
		}
	}
}

func TestKeyPrefix_NeverAdmitsLessThanAnchorFromKey(t *testing.T) {
	// Scoping a listing by the prefix is only safe because it narrows: every key
	// AnchorFromKey claims starts with the prefix, so filtering can drop nothing
	// the ownership test would have kept. The reverse does not hold, which is
	// why the ownership test still runs on what comes back.
	d := OutputDescriptor{AnchorType: "identity", OutputKeyPattern: "cap.roles.{actorSuffix}"}
	prefix, ok := d.KeyPrefix()
	if !ok {
		t.Fatal("expected a usable key prefix")
	}
	for _, actor := range []string{
		"vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
		"vtx.identity.Tswp1AaaaaaaaaaaaaaZ",
	} {
		key := d.BuildKey(actor)
		if !strings.HasPrefix(key, prefix) {
			t.Fatalf("BuildKey(%q) = %q does not start with the scoping prefix %q", actor, key, prefix)
		}
		if _, owned := d.AnchorFromKey(key); !owned {
			t.Fatalf("AnchorFromKey must claim the key BuildKey produced: %q", key)
		}
	}
	// The prefix admits a sibling's key; only AnchorFromKey rejects it.
	sibling := "cap.roles.service.Hj4kPmRtw9nbCxz5vQ2y"
	if !strings.HasPrefix(sibling, prefix) {
		t.Fatalf("expected %q to survive prefix scoping", sibling)
	}
	if _, owned := d.AnchorFromKey(sibling); owned {
		t.Fatalf("AnchorFromKey must still refuse %q", sibling)
	}
}

func TestKeyPrefix_RefusesANatsWildcard(t *testing.T) {
	// `*.{actorSuffix}` scopes to every 2-token key in a shared target — the
	// whole-bucket enumeration the scoping exists to prevent, reached through
	// the mechanism meant to prevent it.
	for _, pattern := range []string{"*.{actorSuffix}", "a.*.{actorSuffix}", "a.>.{actorSuffix}"} {
		d := OutputDescriptor{AnchorType: "identity", OutputKeyPattern: pattern}
		if got, ok := d.KeyPrefix(); ok {
			t.Fatalf("pattern %q returned prefix %q; a wildcard token must be refused", pattern, got)
		}
	}
}

func TestKeyOwnershipRoundTrips_CatchesARepeatedPlaceholder(t *testing.T) {
	// BuildKey substitutes every occurrence; the inverse brackets the first. A
	// descriptor where they disagree renders keys its own orphan direction can
	// never claim.
	good := OutputDescriptor{AnchorType: "identity", OutputKeyPattern: "cap.roles.{actorSuffix}"}
	if !good.KeyOwnershipRoundTrips() {
		t.Fatal("a shipped pattern must round-trip")
	}
	withKeyColumn := OutputDescriptor{AnchorType: "leaseapp", OutputKeyPattern: "leaseExpiry.{actorSuffix}", KeyColumn: "entityId"}
	if !withKeyColumn.KeyOwnershipRoundTrips() {
		t.Fatal("a bare-NanoID (keyColumn) pattern must round-trip too")
	}
	repeated := OutputDescriptor{AnchorType: "identity", OutputKeyPattern: "cap.{actorSuffix}.x.{actorSuffix}"}
	if repeated.KeyOwnershipRoundTrips() {
		t.Fatal("a repeated placeholder must not report a working inverse")
	}
}
