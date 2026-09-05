package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// The seam this file exists for is the one that is green at each layer and
// broken across it: pipeline.SweepStatus → the snapshot copy here →
// health.CapabilityLensStatus / health.LensLivenessStatus. Each side can be
// fully covered by its own package's tests while a value never crosses between
// them, and the only symptom is a clean zero at the surface an operator reads.
//
// Every test below drives the REAL copy functions the two provider closures
// call, not a hand-written transfer that would agree with itself.

// blockedStatus is a sweep snapshot of the shape Sweeper.Status() publishes when
// its standing set holds these classes.
func blockedStatus(retraction, content, unknown, provenance int, worst, reason string) pipeline.SweepStatus {
	return pipeline.SweepStatus{
		Blocked:       retraction + content + unknown + provenance,
		BlockedStreak: 1,
		LastBlocked:   reason,
		BlockedByClass: map[pipeline.BlockedClass]int{
			pipeline.BlockedRetraction: retraction,
			pipeline.BlockedContent:    content,
			pipeline.BlockedUnknown:    unknown,
			pipeline.BlockedProvenance: provenance,
		},
		WorstBlockedClass: worst,
		LastPassAt:        time.Now(),
	}
}

// TestSeam_BlockedClassCensusCrossesBothCopiers carries the same census through
// both planes' copiers. Both, or one surface silently zeroes: the two providers
// are separate closures over separate status structs, and a split copied on one
// of them only leaves the other publishing a census of four zeros — which the
// severity rule reads as a lens holding nothing but unclassifiable rows.
func TestSeam_BlockedClassCensusCrossesBothCopiers(t *testing.T) {
	status := blockedStatus(2, 1, 3, 16, "retraction",
		"stored watermark >= reconciliation token; retraction unrepairable")

	capSnap := health.CapabilityLensStatus{CanonicalName: "capabilityRoles", RuleID: "cap-roles", Status: "active"}
	copyCapabilitySweepStatus(&capSnap, status, time.Minute)
	lensSnap := health.LensLivenessStatus{CanonicalName: "myTasks", RuleID: "lns-myTasks", Status: "active"}
	copyLensSweepStatus(&lensSnap, status, time.Minute)

	for _, got := range []struct {
		plane                                    string
		blocked                                  int
		retraction, content, unknown, provenance int
		worst, reason                            string
	}{
		{"capability", capSnap.SweepBlocked, capSnap.SweepBlockedRetraction, capSnap.SweepBlockedContent,
			capSnap.SweepBlockedUnknown, capSnap.SweepBlockedProvenance, capSnap.SweepWorstBlocked, capSnap.SweepLastBlocked},
		{"plain", lensSnap.SweepBlocked, lensSnap.SweepBlockedRetraction, lensSnap.SweepBlockedContent,
			lensSnap.SweepBlockedUnknown, lensSnap.SweepBlockedProvenance, lensSnap.SweepWorstBlocked, lensSnap.SweepLastBlocked},
	} {
		if got.retraction != 2 || got.content != 1 || got.unknown != 3 || got.provenance != 16 {
			t.Fatalf("%s plane: per-class counts did not cross the seam: %+v", got.plane, got)
		}
		if sum := got.retraction + got.content + got.unknown + got.provenance; sum != got.blocked {
			t.Fatalf("%s plane: per-class counts sum to %d, blocked total is %d", got.plane, sum, got.blocked)
		}
		if got.worst != "retraction" {
			t.Fatalf("%s plane: worst class = %q, want retraction", got.plane, got.worst)
		}
		if !strings.Contains(got.reason, "retraction unrepairable") {
			t.Fatalf("%s plane: the governing text must belong to the governing class: %q", got.plane, got.reason)
		}
	}
}

// TestSeam_AnEmptyBlockedSetCrossesAsEmpty is the discrimination twin: the seam
// must carry the quiet answer too, or a copier could hard-code the loud one and
// still satisfy every assertion above.
func TestSeam_AnEmptyBlockedSetCrossesAsEmpty(t *testing.T) {
	capSnap := health.CapabilityLensStatus{CanonicalName: "capabilityRoles", RuleID: "cap-roles", Status: "active"}
	copyCapabilitySweepStatus(&capSnap, pipeline.SweepStatus{LastPassAt: time.Now()}, time.Minute)

	if capSnap.SweepBlocked != 0 || capSnap.SweepBlockedContent != 0 || capSnap.SweepWorstBlocked != "" {
		t.Fatalf("a lens with nothing blocked must cross as nothing blocked: %+v", capSnap)
	}
}

// TestSeam_EverySweepFieldIsCopied is the gate the two-layer seam actually
// needs, and it is by REFLECTION rather than by a maintained list: a Sweep*
// field added to either health status struct and forgotten in the copier here
// fails by name, instead of publishing a zero that reads as a verdict.
//
// The direction is deliberate. A field the sweep computes and nobody copies is
// invisible — the pipeline test proves the sweep sets it, the health test proves
// the heartbeat evaluates it, and both stay green while the surface reads clean.
func TestSeam_EverySweepFieldIsCopied(t *testing.T) {
	full := pipeline.SweepStatus{
		Reconciled: 7, DivergentStreak: 2, Cursor: "cap.roles.identity.x",
		LastPassAt: time.Now(), Suppression: "rebuild in flight", SuppressionAt: time.Now(),
		FailingActors: 3, FailedStreak: 4, LastFailure: "target write refused",
		Unverified: 5, UnverifiedStreak: 6, LastUnverified: "no retraction transport",
		Blocked: 22, BlockedStreak: 8, LastBlocked: "retraction unrepairable",
		BlockedByClass: map[pipeline.BlockedClass]int{
			pipeline.BlockedRetraction: 2, pipeline.BlockedContent: 1,
			pipeline.BlockedUnknown: 3, pipeline.BlockedProvenance: 16,
		},
		WorstBlockedClass: "retraction",
	}

	capSnap := health.CapabilityLensStatus{}
	copyCapabilitySweepStatus(&capSnap, full, time.Minute)
	lensSnap := health.LensLivenessStatus{}
	copyLensSweepStatus(&lensSnap, full, time.Minute)

	requireEverySweepFieldSet(t, "CapabilityLensStatus", reflect.ValueOf(capSnap))
	requireEverySweepFieldSet(t, "LensLivenessStatus", reflect.ValueOf(lensSnap))
}

func requireEverySweepFieldSet(t *testing.T, structName string, v reflect.Value) {
	t.Helper()
	typ := v.Type()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if !strings.HasPrefix(name, "Sweep") {
			continue
		}
		if v.Field(i).IsZero() {
			t.Errorf("health.%s.%s is left at its zero value by the copier in cmd/refractor.\n\n"+
				"The provider closures build a fresh snapshot per beat and list every field they transfer, so a "+
				"field with no line of its own publishes a zero that reads as a verdict — 'no blocked rows of that "+
				"class', 'never swept' — and nothing fails: the pipeline test proves the sweep computes it and the "+
				"health test proves the heartbeat evaluates it, on either side of a value that never crossed.\n\n"+
				"Add the line to copyCapabilitySweepStatus AND copyLensSweepStatus (cmd/refractor/sweepstatus.go); "+
				"both surfaces publish the same sweep, and a field carried on one of them only is the same defect "+
				"with half the blast radius.", structName, name)
		}
	}
}

// lensLivenessDesignAddedFields names every health.LensLivenessStatus field a
// design has added outside the Sweep* family TestSeam_EverySweepFieldIsCopied
// already covers — the audit's own fields, copied by copyLensAuditStatus
// (cmd/refractor/auditstatus.go), and the plain arm's derivation posture,
// copied by copyLensDerivationStatus (cmd/refractor/derivationstatus.go),
// rather than by a Sweep* copier. A field lands here in the SAME fire that adds
// it to the struct: the alternative is a field with no line of its own
// publishing a zero that reads as a verdict, exactly the failure mode
// TestSeam_EverySweepFieldIsCopied exists to catch for Sweep*.
var lensLivenessDesignAddedFields = []string{
	// secure-plain-lens-retraction-and-audit-design.md Increment 1, §4.1: the
	// columns a Secure Lens's audit comparison excludes.
	"AuditMaskedColumns",
	// The same design's Increment 2, §4.2 and §5: whether the plain arm's
	// derivation — a retraction transport on this plane — is ELIGIBLE at all
	// (a fixed property of the lens's shape), whether it is currently ARMED
	// (Eligible AND the licence), how often it fell back to the rescan that
	// retracts nothing, and how far the last refused derived set overshot the
	// cap.
	"DerivationEligible",
	"DerivationArmed",
	"DerivationFellBack",
	"DerivationOverCapSize",
}

// TestLensLivenessStatus_NewFieldsAreCarried is the reflection gate
// TestSeam_EverySweepFieldIsCopied's own comment calls for widening: every
// field named in lensLivenessDesignAddedFields must survive a non-trivial
// status crossing the copier that owns it. It fails by NAME, so a field added
// to the struct and forgotten in a copier is caught here rather than publishing
// a silent zero at the only surface anyone would notice.
//
// Both copiers run against ONE snapshot, because the field list is one list and
// the defect is "no line copies this field", whichever copier the line belongs
// in — a per-copier split would need the list split too, and a field added to
// the wrong half would then pass.
func TestLensLivenessStatus_NewFieldsAreCarried(t *testing.T) {
	full := pipeline.AuditStatus{
		Enrolled:       true,
		MaskedColumns:  []string{"name", "email"},
		Audited:        10,
		Divergent:      map[string]int{"stale": 1},
		DivergentTotal: 1,
		CoverageBasis:  pipeline.AuditCoverageBasisKeyType,
		ListingSize:    64,
		LastPassAt:     time.Now(),
	}
	derivation := pipeline.PlainDerivationStatus{Eligible: true, Armed: true, FellBack: 3, OverCapSize: 91}

	lensSnap := health.LensLivenessStatus{}
	copyLensAuditStatus(&lensSnap, full, time.Minute)
	copyLensDerivationStatus(&lensSnap, derivation)

	v := reflect.ValueOf(lensSnap)
	typ := v.Type()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if !slices.Contains(lensLivenessDesignAddedFields, name) {
			continue
		}
		if v.Field(i).IsZero() {
			t.Errorf("health.LensLivenessStatus.%s is left at its zero value by the copier that owns it.\n\n"+
				"This field is named in lensLivenessDesignAddedFields (cmd/refractor/sweepstatus_test.go) as "+
				"one a design added outside the Sweep* family; add its line to copyLensAuditStatus "+
				"(cmd/refractor/auditstatus.go) or copyLensDerivationStatus "+
				"(cmd/refractor/derivationstatus.go).", name)
		}
	}
}
