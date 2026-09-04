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

func TestAnchorFromKey_PerEntry_RoundTripsAnEntryKey(t *testing.T) {
	// A perEntry descriptor's real keys always carry one trailing entry token
	// beyond BuildKey(actor) alone (§4.4 of
	// cap-read-per-anchor-grant-keys-design.md); the inverse must recover the
	// owning actor from that shape, not the bare BuildKey(actor) prefix.
	d := OutputDescriptor{
		AnchorType:       "identity",
		OutputKeyPattern: "cap-read.roles.{actorSuffix}",
		EntryKeyColumn:   "anchorId",
	}
	actor := "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"
	entryID := "Kx3TmZpq7RvwNsY2Hc9L"
	key := d.BuildKey(actor) + "." + entryID

	got, ok := d.AnchorFromKey(key)
	if !ok {
		t.Fatalf("AnchorFromKey rejected a well-formed perEntry key %q", key)
	}
	if got != actor {
		t.Fatalf("round trip: got %q, want %q", got, actor)
	}
}

func TestAnchorFromKey_PerEntry_RejectsAKeyWithNoTrailingEntryToken(t *testing.T) {
	// BuildKey(actor) alone is never a real perEntry key — accepting it as one
	// would let the coverage direction see a bare actor prefix (never written
	// by EntryEnvelopeFn) as evidence of a live row.
	d := OutputDescriptor{AnchorType: "identity", OutputKeyPattern: "cap-read.roles.{actorSuffix}", EntryKeyColumn: "anchorId"}
	actor := "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"
	if _, ok := d.AnchorFromKey(d.BuildKey(actor)); ok {
		t.Fatal("a bare BuildKey(actor) prefix must not parse as a perEntry key")
	}
}

func TestAnchorFromKey_PerEntry_RejectsANonNanoIDTrailingToken(t *testing.T) {
	d := OutputDescriptor{AnchorType: "identity", OutputKeyPattern: "cap-read.roles.{actorSuffix}", EntryKeyColumn: "anchorId"}
	actor := "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"
	if _, ok := d.AnchorFromKey(d.BuildKey(actor) + ".not-a-nanoid"); ok {
		t.Fatal("a malformed trailing entry token must be refused")
	}
}

func TestAnchorFromKey_PerEntry_RejectsAForeignAnchorType(t *testing.T) {
	// The shared-bucket exactness guarantee must survive the extra trailing
	// segment: a sibling lens sharing this lens's prefix but a different
	// anchor type must still be refused.
	d := OutputDescriptor{AnchorType: "identity", OutputKeyPattern: "cap-read.roles.{actorSuffix}", EntryKeyColumn: "anchorId"}
	foreign := "cap-read.roles.service.Hj4kPmRtw9nbCxz5vQ2y.Kx3TmZpq7RvwNsY2Hc9L"
	if _, ok := d.AnchorFromKey(foreign); ok {
		t.Fatal("a different anchor type must be refused")
	}
}

func TestKeyOwnershipRoundTrips_PerEntryDescriptorRoundTrips(t *testing.T) {
	// The sweep's install gate (sweepEnrolment) probes this before enrolling
	// any lens; a perEntry descriptor must pass it the same as a doc-mode one.
	d := OutputDescriptor{AnchorType: "identity", OutputKeyPattern: "cap-read.roles.{actorSuffix}", EntryKeyColumn: "anchorId"}
	if !d.KeyOwnershipRoundTrips() {
		t.Fatal("a well-formed perEntry descriptor must round-trip")
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

// AnchorEntryFromKey is the inverse the read-grant change edge is wired with.
// It answers the actor half exactly as AnchorFromKey does, plus the one anchor
// whose grant a per-entry key's row is about — the value that decides how
// narrowly the personal plane republishes.

func TestAnchorEntryFromKey_PerEntry_RecoversTheTrailingAnchor(t *testing.T) {
	d := OutputDescriptor{
		AnchorType:       "identity",
		OutputKeyPattern: "cap-read.{actorSuffix}",
		EntryKeyColumn:   "anchorId",
	}
	actor := "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"
	entryID := "Kx3TmZpq7RvwNsY2Hc9L"
	key := d.BuildKey(actor) + "." + entryID

	gotActor, gotEntry, ok := d.AnchorEntryFromKey(key)
	if !ok {
		t.Fatalf("AnchorEntryFromKey rejected a well-formed perEntry key %q", key)
	}
	if gotActor != actor {
		t.Fatalf("actor: got %q, want %q", gotActor, actor)
	}
	// Compared against the key's own trailing segment rather than a constant,
	// so an inverse that returned some other NanoID would fail here.
	if want := key[strings.LastIndexByte(key, '.')+1:]; gotEntry != want {
		t.Fatalf("entry: got %q, want the key's trailing segment %q", gotEntry, want)
	}
}

func TestAnchorEntryFromKey_DocMode_NamesNoAnchor(t *testing.T) {
	// A descriptor writing one document per actor names no single anchor, so
	// the empty token is the honest answer and its consumer reads it as "the
	// whole actor moved".
	d := OutputDescriptor{AnchorType: "identity", OutputKeyPattern: "cap.roles.{actorSuffix}"}
	actor := "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"

	gotActor, gotEntry, ok := d.AnchorEntryFromKey(d.BuildKey(actor))
	if !ok {
		t.Fatalf("AnchorEntryFromKey rejected this descriptor's own key")
	}
	if gotActor != actor {
		t.Fatalf("actor: got %q, want %q", gotActor, actor)
	}
	if gotEntry != "" {
		t.Fatalf("entry: got %q, want empty — this key names no single anchor", gotEntry)
	}
}

func TestAnchorEntryFromKey_KeyColumn_NamesNoAnchor(t *testing.T) {
	// A keyColumn descriptor's key ALSO names the actor alone: its trailing
	// segment is the actor's own bare NanoID, with the type segment supplied by
	// the descriptor. Returning it as an entry token would hand the change edge
	// a scope naming the actor as though it were an anchor — a set that matches
	// no row, withholding every one of them behind a frame that still names them.
	d := OutputDescriptor{
		AnchorType:       "leaseapp",
		OutputKeyPattern: "leaseApplicationComplete.{actorSuffix}",
		KeyColumn:        "entityId",
	}
	actor := "vtx.leaseapp.Lk2Pn6mQrtwzKbcXvP3T"
	key := d.BuildKey(actor)

	gotActor, gotEntry, ok := d.AnchorEntryFromKey(key)
	if !ok {
		t.Fatalf("AnchorEntryFromKey rejected this descriptor's own key %q", key)
	}
	if gotActor != actor {
		t.Fatalf("actor: got %q, want %q", gotActor, actor)
	}
	if gotEntry != "" {
		t.Fatalf("entry: got %q, want empty — the trailing segment is the actor's own id, not an entry", gotEntry)
	}
}

func TestAnchorEntryFromKey_AgreesWithAnchorFromKey(t *testing.T) {
	// The two must claim the same keys: AnchorFromKey is the convergence
	// sweep's ownership test and AnchorEntryFromKey the change edge's routing,
	// and a key one claimed and the other refused would make a row owned by
	// the sweep unroutable by the edge.
	//
	// AnchorFromKey delegates to AnchorEntryFromKey today, so this passes by
	// construction. It is here for the re-implementation that stops delegating:
	// two inversions of one key shape drift, and the drift is silent on the wire.
	descs := []OutputDescriptor{
		{AnchorType: "identity", OutputKeyPattern: "cap.roles.{actorSuffix}"},
		{AnchorType: "identity", OutputKeyPattern: "cap-read.{actorSuffix}", EntryKeyColumn: "anchorId"},
		{AnchorType: "leaseapp", OutputKeyPattern: "leaseApplicationComplete.{actorSuffix}", KeyColumn: "entityId"},
	}
	keys := []string{
		"cap.roles.identity.Hj4kPmRtw9nbCxz5vQ2y",
		"cap-read.identity.Hj4kPmRtw9nbCxz5vQ2y.Kx3TmZpq7RvwNsY2Hc9L",
		"leaseApplicationComplete.Lk2Pn6mQrtwzKbcXvP3T",
		"cap.roles.service.Hj4kPmRtw9nbCxz5vQ2y",
		"cap-read.identity.Hj4kPmRtw9nbCxz5vQ2y",
		"",
	}
	for _, d := range descs {
		for _, key := range keys {
			wantActor, wantOK := d.AnchorFromKey(key)
			gotActor, _, gotOK := d.AnchorEntryFromKey(key)
			if wantOK != gotOK || wantActor != gotActor {
				t.Fatalf("pattern %q key %q: AnchorFromKey = (%q,%v) but AnchorEntryFromKey = (%q,%v)",
					d.OutputKeyPattern, key, wantActor, wantOK, gotActor, gotOK)
			}
		}
	}
}
