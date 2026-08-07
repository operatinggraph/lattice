// BackfillAppointmentSite integration tests for the clinic-domain Capability
// Package — the orchestration-internal auto-remediation twin of
// CreateAppointment's own optional site branch (clinicSiteBackfill's
// missing_site gap, lenses.go/targets.go), mirroring cafe-domain's own
// BackfillTabStaleAt integration coverage.
//
// Same external test package / harness as integration_test.go and
// site_integration_test.go: seed the kernel, install rbac+identity+hygiene +
// location-domain + clinic-domain, then submit BackfillAppointmentSite and
// assert the committed atSite link (or its deliberate absence).
//
// Coverage:
//  1. TestClinic_BackfillAppointmentSite_SingleSite   — backfills when the provider practicesAt exactly one site
//  2. TestClinic_BackfillAppointmentSite_NoopAlreadyHasSite — no-ops (site unchanged) when the appointment already carries a live atSite link
//  3. TestClinic_BackfillAppointmentSite_NoopAmbiguousSites — no-ops (never guesses) when the provider practicesAt zero or two-or-more sites
//  4. TestClinic_BackfillAppointmentSite_Idempotent   — a second dispatch after a successful backfill is a clean no-op
package clinicdomain_test

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// atSiteLinkKey is the deterministic per-(appointment, building) atSite link
// key (mirrors practicesAtLinkKey, site_integration_test.go).
func atSiteLinkKey(apptKey, buildingKey string) string {
	_, aid, _ := substrate.ParseVertexKey(apptKey)
	_, bid, _ := substrate.ParseVertexKey(buildingKey)
	return "lnk.appointment." + aid + ".atSite.building." + bid
}

// TestClinic_BackfillAppointmentSite_SingleSite proves the core backfill
// path: an appointment booked with no site, whose provider practicesAt
// EXACTLY ONE site, gets that site's atSite link written on dispatch — the
// same mutation CreateAppointment's own site branch would have written had a
// site been supplied at booking time.
func TestClinic_BackfillAppointmentSite_SingleSite(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "backfill-single")

	patientKey := createPatient(t, ctx, conn, cp, cons, "bfspat0001", "Sam Backfill")
	providerKey := createProvider(t, ctx, conn, cp, cons, "bfsprv0001", "Dr. Backfill", "Cardiology")
	buildingKey := clCreateBuilding(t, ctx, conn, cp, cons, "bfsbld0001")
	clSubmit(t, ctx, conn, cp, cons, "bfsset0001", "SetSiteProfile", "clinicSite",
		`{"buildingKey":"`+buildingKey+`","name":"Backfill Clinic"}`, []string{buildingKey}, processor.OutcomeAccepted)
	assignProviderSite(t, ctx, conn, cp, cons, "bfsasg0001", providerKey, buildingKey, processor.OutcomeAccepted)

	// Booked with NO site — the pre-existing-corpus shape this op remediates.
	apptID := clSubmit(t, ctx, conn, cp, cons, "bfsappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-01T15:00:00Z","endsAt":"2026-07-01T15:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	if !clMissing(t, ctx, conn, atSiteLinkKey(apptKey, buildingKey)) {
		t.Fatalf("appointment must not carry an atSite link before backfill")
	}

	clSubmit(t, ctx, conn, cp, cons, "bfsback0001", "BackfillAppointmentSite", "appointment",
		`{"appointmentKey":"`+apptKey+`"}`, []string{apptKey}, processor.OutcomeAccepted)

	doc := clReadDoc(t, ctx, conn, atSiteLinkKey(apptKey, buildingKey))
	if doc["class"] != "atSite" {
		t.Fatalf("atSite link class = %v, want atSite", doc["class"])
	}
	if del, _ := doc["isDeleted"].(bool); del {
		t.Fatalf("atSite link should be alive; got isDeleted=%v", del)
	}
	if sv, _ := doc["sourceVertex"].(string); sv != apptKey {
		t.Fatalf("link sourceVertex = %q, want %q (the appointment)", sv, apptKey)
	}
	if tv, _ := doc["targetVertex"].(string); tv != buildingKey {
		t.Fatalf("link targetVertex = %q, want %q (the site)", tv, buildingKey)
	}
}

// TestClinic_BackfillAppointmentSite_NoopAlreadyHasSite proves a no-op:
// dispatching against an appointment that already carries a live atSite link
// (a normal CreateAppointment{site} booking) leaves that link untouched
// rather than rewriting or duplicating it.
func TestClinic_BackfillAppointmentSite_NoopAlreadyHasSite(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "backfill-hassite")

	patientKey := createPatient(t, ctx, conn, cp, cons, "bfhpat0001", "Has Site Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "bfhprv0001", "Dr. Has Site", "Cardiology")
	buildingKey := clCreateBuilding(t, ctx, conn, cp, cons, "bfhbld0001")
	clSubmit(t, ctx, conn, cp, cons, "bfhset0001", "SetSiteProfile", "clinicSite",
		`{"buildingKey":"`+buildingKey+`","name":"Has Site Clinic"}`, []string{buildingKey}, processor.OutcomeAccepted)
	assignProviderSite(t, ctx, conn, cp, cons, "bfhasg0001", providerKey, buildingKey, processor.OutcomeAccepted)

	apptID := clCreateAppointmentWithSite(t, ctx, conn, cp, cons, "bfhappt0001", patientKey, providerKey, buildingKey, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	lk := atSiteLinkKey(apptKey, buildingKey)

	clSubmit(t, ctx, conn, cp, cons, "bfhback0001", "BackfillAppointmentSite", "appointment",
		`{"appointmentKey":"`+apptKey+`"}`, []string{apptKey}, processor.OutcomeAccepted)

	after := clReadDoc(t, ctx, conn, lk)
	if del, _ := after["isDeleted"].(bool); del {
		t.Fatalf("atSite link should remain alive after a no-op backfill; got isDeleted=%v", del)
	}
	if tv, _ := after["targetVertex"].(string); tv != buildingKey {
		t.Fatalf("atSite link target changed by a no-op backfill: got %q, want %q", tv, buildingKey)
	}
}

// TestClinic_BackfillAppointmentSite_NoopAmbiguousSites proves the op never
// guesses: an appointment whose provider practicesAt ZERO sites, or TWO or
// more, is left with no atSite link — the same sole-site fallback semantics
// the booking UI's own client-side site auto-fill applies.
func TestClinic_BackfillAppointmentSite_NoopAmbiguousSites(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "backfill-ambiguous")

	t.Run("zero sites", func(t *testing.T) {
		patientKey := createPatient(t, ctx, conn, cp, cons, "bfzpat0001", "Zero Site Patient")
		providerKey := createProvider(t, ctx, conn, cp, cons, "bfzprv0001", "Dr. No Site", "Cardiology")

		apptID := clSubmit(t, ctx, conn, cp, cons, "bfzappt0001", "CreateAppointment", "appointment",
			`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-02T15:00:00Z","endsAt":"2026-07-02T15:30:00Z"}`,
			[]string{patientKey, providerKey}, processor.OutcomeAccepted)
		apptKey := "vtx.appointment." + apptID

		// The provider practicesAt no buildings at all — a clean no-op, proven
		// by the op itself returning Accepted (never a hard failure) since there
		// is no candidate site key to assert absence against in this shape.
		clSubmit(t, ctx, conn, cp, cons, "bfzback0001", "BackfillAppointmentSite", "appointment",
			`{"appointmentKey":"`+apptKey+`"}`, []string{apptKey}, processor.OutcomeAccepted)
	})

	t.Run("two sites", func(t *testing.T) {
		patientKey := createPatient(t, ctx, conn, cp, cons, "bftpat0001", "Two Site Patient")
		providerKey := createProvider(t, ctx, conn, cp, cons, "bftprv0001", "Dr. Two Sites", "Cardiology")
		buildingA := clCreateBuilding(t, ctx, conn, cp, cons, "bftbldA001")
		buildingB := clCreateBuilding(t, ctx, conn, cp, cons, "bftbldB001")
		assignProviderSite(t, ctx, conn, cp, cons, "bftasgA001", providerKey, buildingA, processor.OutcomeAccepted)
		assignProviderSite(t, ctx, conn, cp, cons, "bftasgB001", providerKey, buildingB, processor.OutcomeAccepted)

		apptID := clSubmit(t, ctx, conn, cp, cons, "bftappt0001", "CreateAppointment", "appointment",
			`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-03T15:00:00Z","endsAt":"2026-07-03T15:30:00Z"}`,
			[]string{patientKey, providerKey}, processor.OutcomeAccepted)
		apptKey := "vtx.appointment." + apptID

		clSubmit(t, ctx, conn, cp, cons, "bftback0001", "BackfillAppointmentSite", "appointment",
			`{"appointmentKey":"`+apptKey+`"}`, []string{apptKey}, processor.OutcomeAccepted)

		for _, b := range []string{buildingA, buildingB} {
			if !clMissing(t, ctx, conn, atSiteLinkKey(apptKey, b)) {
				t.Fatalf("no atSite link should be written for either candidate site %s when the provider has two", b)
			}
		}
	})
}

// TestClinic_BackfillAppointmentSite_Idempotent proves a second dispatch
// after a successful backfill is a clean no-op: the atSite link stays as the
// first dispatch left it, never duplicated or rewritten.
func TestClinic_BackfillAppointmentSite_Idempotent(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "backfill-idempotent")

	patientKey := createPatient(t, ctx, conn, cp, cons, "bfipat0001", "Idempotent Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "bfiprv0001", "Dr. Idempotent", "Cardiology")
	buildingKey := clCreateBuilding(t, ctx, conn, cp, cons, "bfibld0001")
	clSubmit(t, ctx, conn, cp, cons, "bfiset0001", "SetSiteProfile", "clinicSite",
		`{"buildingKey":"`+buildingKey+`","name":"Idempotent Clinic"}`, []string{buildingKey}, processor.OutcomeAccepted)
	assignProviderSite(t, ctx, conn, cp, cons, "bfiasg0001", providerKey, buildingKey, processor.OutcomeAccepted)

	apptID := clSubmit(t, ctx, conn, cp, cons, "bfiappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-04T15:00:00Z","endsAt":"2026-07-04T15:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	lk := atSiteLinkKey(apptKey, buildingKey)

	clSubmit(t, ctx, conn, cp, cons, "bfiback0001", "BackfillAppointmentSite", "appointment",
		`{"appointmentKey":"`+apptKey+`"}`, []string{apptKey}, processor.OutcomeAccepted)
	first := clReadDoc(t, ctx, conn, lk)

	// Re-dispatch (redelivery / a second convergence pass before the lens
	// re-projects) — must no-op cleanly, never re-create or duplicate the link.
	clSubmit(t, ctx, conn, cp, cons, "bfiback0002", "BackfillAppointmentSite", "appointment",
		`{"appointmentKey":"`+apptKey+`"}`, []string{apptKey}, processor.OutcomeAccepted)
	second := clReadDoc(t, ctx, conn, lk)

	if del, _ := second["isDeleted"].(bool); del {
		t.Fatalf("atSite link should remain alive after a re-dispatch; got isDeleted=%v", del)
	}
	if first["targetVertex"] != second["targetVertex"] {
		t.Fatalf("atSite link target changed across re-dispatch: %v -> %v", first["targetVertex"], second["targetVertex"])
	}
}
