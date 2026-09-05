package health

// The reader for the plain arm's neighbour-anchor derivation tally. On a plain
// lens the derivation is a RETRACTION transport — an armed lens's seeded
// per-anchor evaluation is what emits the Delete an upsert-only rescan cannot
// (secure-plain-lens-retraction-and-audit-design.md §4.2) — so a lens that has
// fallen back to the rescan is a lens whose retractions are not being made, and
// nothing else in the snapshot says so.
//
// The presence gate is Eligible, not Armed: an eligible lens publishes its
// tally whether or not the licence currently admits it, because Armed false
// still means "declared, currently off" rather than "no transport at all" —
// and a static licence refusal never moves the tally, so its absence would be
// indistinguishable from an armed lens that has simply never fallen back. An
// INELIGIBLE lens publishes neither key at all: its shape can never support
// the transport, so nothing here means anything for it.

import (
	"testing"
	"time"
)

func derivationSnap(name string, eligible, armed bool, fellBack uint64, overCap int) LensLivenessStatus {
	return LensLivenessStatus{
		CanonicalName:         name,
		RuleID:                "lns-" + name,
		Status:                "active",
		DerivationEligible:    eligible,
		DerivationArmed:       armed,
		DerivationFellBack:    fellBack,
		DerivationOverCapSize: overCap,
	}
}

// An eligible, armed lens publishes derivationArmed:true and both numbers,
// fall-backs and all — the operator-facing half of the cap: a lens whose
// neighbourhoods routinely outgrow it is a cap-sizing finding, and only the
// size says how far past it they reach.
func TestEvalLenses_EligibleArmedDerivationPublishesItsTally(t *testing.T) {
	h := &LatticeHeartbeater{}
	metric, _ := beat(h, time.Now(), derivationSnap("renewalsRead", true, true, 4, 91))

	if got := metric["renewalsRead"]["derivationArmed"]; got != true {
		t.Fatalf("an armed lens must publish derivationArmed:true; got %v", got)
	}
	if got := metric["renewalsRead"]["derivationFellBack"]; got != uint64(4) {
		t.Fatalf("the fall-back count must reach the metric; got %v", got)
	}
	if got := metric["renewalsRead"]["derivationOverCapSize"]; got != 91 {
		t.Fatalf("and the refused derived set's size with it; got %v", got)
	}
}

// An eligible lens the licence currently refuses publishes derivationArmed:
// false rather than nothing — "declared, currently off" — and its fall-back
// count is still readable: a static licence refusal never moves the tally, so
// a count that has already accrued must stay on the wire regardless of
// whether the licence admits the lens at THIS beat.
func TestEvalLenses_EligibleUnarmedDerivationPublishesArmedFalse(t *testing.T) {
	h := &LatticeHeartbeater{}
	metric, _ := beat(h, time.Now(), derivationSnap("clinicEncountersRead", true, false, 4, 0))

	got, present := metric["clinicEncountersRead"]["derivationArmed"]
	if !present {
		t.Fatal("an eligible lens must publish derivationArmed even while unarmed")
	}
	if got != false {
		t.Fatalf("and it must read false; got %v", got)
	}
	fellBack, present := metric["clinicEncountersRead"]["derivationFellBack"]
	if !present {
		t.Fatal("the fall-back count must stay readable while the licence refuses the lens")
	}
	if fellBack != uint64(4) {
		t.Fatalf("and it must carry whatever has already accrued; got %v", fellBack)
	}
}

// An eligible lens that has never fallen back publishes a ZERO fall-back count
// rather than nothing: the transport's shape is declared and the tally has
// simply held.
func TestEvalLenses_EligibleDerivationPublishesZeroFellBackRatherThanNothing(t *testing.T) {
	h := &LatticeHeartbeater{}
	metric, _ := beat(h, time.Now(), derivationSnap("clinicEncountersRead", true, true, 0, 0))

	got, present := metric["clinicEncountersRead"]["derivationFellBack"]
	if !present {
		t.Fatal("an eligible lens with no fall-backs must publish the zero; absence is reserved for an ineligible lens")
	}
	if got != uint64(0) {
		t.Fatalf("and it must be zero; got %v", got)
	}
}

// The over-cap size is carried only once it has fired — a lens that has never
// exceeded the cap has no size to report, and a permanent zero beside a
// non-zero fellBack (whose causes include a failed walk as well as a declined
// one) would misread as "every fall-back was an over-cap one of size nothing".
func TestEvalLenses_ZeroOverCapSizeIsAbsent(t *testing.T) {
	h := &LatticeHeartbeater{}
	metric, _ := beat(h, time.Now(), derivationSnap("renewalsRead", true, true, 2, 0))

	if _, present := metric["renewalsRead"]["derivationOverCapSize"]; present {
		t.Fatal("a lens that has never exceeded the cap must not publish a size")
	}
}

// An INELIGIBLE lens publishes none of the three keys. This is the
// discrimination vector: the group is meaningless for a lens whose shape can
// never support the transport, and a permanent zero or a false there would
// read as one of the verdicts above.
func TestEvalLenses_IneligibleDerivationPublishesNothing(t *testing.T) {
	h := &LatticeHeartbeater{}
	metric, _ := beat(h, time.Now(), derivationSnap("clinicPatientsRead", false, false, 0, 0))

	for _, key := range []string{"derivationArmed", "derivationFellBack", "derivationOverCapSize"} {
		if _, present := metric["clinicPatientsRead"][key]; present {
			t.Fatalf("%s must be absent for an ineligible lens", key)
		}
	}
	if got := metric["clinicPatientsRead"]["alert"]; got != "ok" {
		t.Fatalf("the tally is a measurement, not a verdict, and raises no alert; got %v", got)
	}
}
