package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakeKV builds a kvGetter over an in-memory map for the compute* seam tests —
// the same headless seam loftspace-app's computeListings tests use. A key absent
// from the map reports (nil, false), exercising the tombstone-skip path.
func fakeKV(entries map[string]any) (keys []string, get kvGetter) {
	raw := map[string][]byte{}
	for k, v := range entries {
		b, _ := json.Marshal(v)
		raw[k] = b
		keys = append(keys, k)
	}
	get = func(key string) ([]byte, bool) {
		b, ok := raw[key]
		return b, ok
	}
	return keys, get
}

func TestComputeProviders_SortsAndSkips(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"vtx.provider.B": map[string]any{"providerKey": "vtx.provider.B", "name": "Dr. Sam Okafor", "specialty": "Cardiology", "credentials": "MD",
			"timeOff": []any{map[string]any{"from": "2026-07-01T00:00:00Z", "to": "2026-07-06T00:00:00Z", "reason": "Vacation"}},
			"hours":   []any{map[string]any{"day": 1, "openSec": 32400, "closeSec": 61200}}},
		"vtx.provider.A": map[string]any{"providerKey": "vtx.provider.A", "name": "Dr. Lee", "specialty": "Dermatology"},
		// A tombstoned projection row with no providerKey must be skipped.
		"vtx.provider.X": map[string]any{"name": "Ghost"},
	})
	rows := computeProviders(keys, get)
	if len(rows) != 2 {
		t.Fatalf("expected 2 providers (the keyless row skipped), got %d", len(rows))
	}
	if rows[0].Name != "Dr. Lee" || rows[1].Name != "Dr. Sam Okafor" {
		t.Fatalf("providers not sorted by name: %+v", rows)
	}
	if rows[1].Credentials != "MD" {
		t.Fatalf("credentials lost: %+v", rows[1])
	}
	// The provider with no timeOff projects an empty slice; the other round-trips
	// its blackout range (the time-off manager reads this to seed its draft).
	if len(rows[0].TimeOff) != 0 {
		t.Fatalf("expected no time-off for Dr. Lee, got %+v", rows[0].TimeOff)
	}
	if len(rows[1].TimeOff) != 1 || rows[1].TimeOff[0].Reason != "Vacation" || rows[1].TimeOff[0].From != "2026-07-01T00:00:00Z" {
		t.Fatalf("time-off range lost: %+v", rows[1].TimeOff)
	}
	// The provider with no hours projects an empty slice; the other round-trips its
	// availability window (the booking slot picker reads this to compute open slots).
	if len(rows[0].Hours) != 0 {
		t.Fatalf("expected no hours for Dr. Lee, got %+v", rows[0].Hours)
	}
	if len(rows[1].Hours) != 1 || rows[1].Hours[0].Day != 1 || rows[1].Hours[0].OpenSec != 32400 || rows[1].Hours[0].CloseSec != 61200 {
		t.Fatalf("hours window lost: %+v", rows[1].Hours)
	}
}

func TestComputePatients_NameOnlySortedSkips(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"vtx.patient.B": map[string]any{"patientKey": "vtx.patient.B", "name": "Bob Tenant"},
		"vtx.patient.A": map[string]any{"patientKey": "vtx.patient.A", "name": "Alice Rivera"},
		"vtx.patient.X": map[string]any{"name": "Keyless"}, // skipped
	})
	rows := computePatients(keys, get)
	if len(rows) != 2 {
		t.Fatalf("expected 2 patients, got %d", len(rows))
	}
	if rows[0].Name != "Alice Rivera" || rows[1].Name != "Bob Tenant" {
		t.Fatalf("patients not sorted by name: %+v", rows)
	}
}

func TestComputeAvailability_ScopeByProvider(t *testing.T) {
	keys, get := apptFixture()
	rows := computeAvailability(keys, get, "vtx.provider.sam")
	if len(rows) != 2 {
		t.Fatalf("expected 2 appointments on Dr. Sam's schedule, got %d", len(rows))
	}
	// Sorted by startsAt: the 10:00 (bob) before the 15:00 (alice).
	if rows[0].StartsAt != "2026-07-01T10:00:00Z" || rows[1].StartsAt != "2026-07-01T15:00:00Z" {
		t.Fatalf("appointments not sorted by startsAt: %+v", rows)
	}
	for _, r := range rows {
		if r.AppointmentKey == "" {
			t.Fatalf("expected an appointmentKey on every row: %+v", r)
		}
	}
}

func TestComputeAvailability_OmitsPatientAndVisitFields(t *testing.T) {
	keys, get := apptFixture()
	rows := computeAvailability(keys, get, "vtx.provider.sam")
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, leak := range []string{"patientKey", "patientName", "providerKey", "providerName", "reason", "documentedAt", "followUpRequested", "followUpDate", "reminderSentAt"} {
		if strings.Contains(body, leak) {
			t.Fatalf("availabilityRow leaked %q into the unauthenticated response: %s", leak, body)
		}
	}
}

func TestComputeAvailability_EmptyProviderMatchesNothing(t *testing.T) {
	keys, get := apptFixture()
	rows := computeAvailability(keys, get, "")
	if len(rows) != 0 {
		t.Fatalf("expected no rows for an empty provider scope, got %+v", rows)
	}
}

// apptFixture builds three appointments: alice has one with Dr. Sam (15:00) and
// one with Dr. Lee (09:00); bob has one with Dr. Sam (10:00). Plus a keyless
// tombstone row that must be skipped.
func apptFixture() ([]string, kvGetter) {
	return fakeKV(map[string]any{
		"vtx.appointment.1": map[string]any{
			"appointmentKey": "vtx.appointment.1", "startsAt": "2026-07-01T15:00:00Z", "endsAt": "2026-07-01T15:30:00Z",
			"status": "scheduled", "patientKey": "vtx.patient.alice", "patientName": "Alice Rivera",
			"providerKey": "vtx.provider.sam", "providerName": "Dr. Sam Okafor", "providerSpecialty": "Cardiology",
		},
		"vtx.appointment.2": map[string]any{
			"appointmentKey": "vtx.appointment.2", "startsAt": "2026-07-01T09:00:00Z", "endsAt": "2026-07-01T09:20:00Z",
			"status": "confirmed", "patientKey": "vtx.patient.alice", "patientName": "Alice Rivera",
			"providerKey": "vtx.provider.lee", "providerName": "Dr. Lee", "providerSpecialty": "Dermatology",
		},
		"vtx.appointment.3": map[string]any{
			"appointmentKey": "vtx.appointment.3", "startsAt": "2026-07-01T10:00:00Z", "endsAt": "2026-07-01T10:30:00Z",
			"status": "scheduled", "patientKey": "vtx.patient.bob", "patientName": "Bob Tenant",
			"providerKey": "vtx.provider.sam", "providerName": "Dr. Sam Okafor", "providerSpecialty": "Cardiology",
		},
		"vtx.appointment.x": map[string]any{"startsAt": "2026-07-01T08:00:00Z"}, // keyless → skipped
	})
}

// TestBuildEnvelope_Defaults pins the op-envelope seam the FE relies on: a missing
// lane defaults to "default", an empty payload to "{}", and reads carry through.
func TestBuildEnvelope_Defaults(t *testing.T) {
	env, err := buildEnvelope(opRequest{OperationType: "CreateAppointment", Reads: []string{"vtx.patient.a", "vtx.provider.b", "", "vtx.patient.a"}}, "req1", "vtx.identity.admin", time.Time{})
	if err != nil {
		t.Fatalf("buildEnvelope: %v", err)
	}
	if env.Lane != "default" {
		t.Fatalf("lane default = %q, want default", env.Lane)
	}
	if string(env.Payload) != "{}" {
		t.Fatalf("payload default = %q, want {}", string(env.Payload))
	}
	if env.ContextHint == nil || len(env.ContextHint.Reads) != 2 {
		t.Fatalf("reads not cleaned/deduped: %+v", env.ContextHint)
	}
}

func TestBuildEnvelope_RequiresOperationType(t *testing.T) {
	if _, err := buildEnvelope(opRequest{}, "req", "actor", time.Time{}); err == nil {
		t.Fatalf("expected an error for a missing operationType")
	}
}
