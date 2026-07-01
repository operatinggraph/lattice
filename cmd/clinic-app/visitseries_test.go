package main

import "testing"

func TestComputeVisitSeries_PrefixFiltersSortsSkips(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"visitSeriesDue.vtx.visitseries.B": map[string]any{
			"entityKey": "vtx.visitseries.B", "patientKey": "vtx.patient.bob", "providerKey": "vtx.provider.lee",
			"intervalDays": 30, "nextDueAt": "2026-08-01T09:00:00Z", "occurrenceCount": 0,
			"active": true, "missing_series_advance": false,
		},
		"visitSeriesDue.vtx.visitseries.A": map[string]any{
			"entityKey": "vtx.visitseries.A", "patientKey": "vtx.patient.alice", "providerKey": "vtx.provider.sam",
			"intervalDays": 7, "nextDueAt": "2026-07-15T09:00:00Z", "occurrenceCount": 2,
			"active": true, "missing_series_advance": true,
		},
		// A row under a different weaver-target prefix sharing the bucket must be
		// ignored — the bucket is shared across packages' targets.
		"followUpReminders.vtx.appointment.X": map[string]any{"entityKey": "vtx.appointment.X"},
		// A tombstoned projection row with no entityKey must be skipped.
		"visitSeriesDue.vtx.visitseries.ghost": map[string]any{"patientKey": "vtx.patient.ghost"},
	})
	rows := computeVisitSeries(keys, get)
	if len(rows) != 2 {
		t.Fatalf("expected 2 series (other-prefix + keyless rows skipped), got %d: %+v", len(rows), rows)
	}
	if rows[0].EntityKey != "vtx.visitseries.A" || rows[1].EntityKey != "vtx.visitseries.B" {
		t.Fatalf("series not sorted by nextDueAt: %+v", rows)
	}
	if !rows[0].MissingSeriesAdvance || rows[1].MissingSeriesAdvance {
		t.Fatalf("missing_series_advance decode lost: %+v", rows)
	}
	if rows[0].OccurrenceCount != 2 || rows[1].IntervalDays != 30 {
		t.Fatalf("int columns decode lost: %+v", rows)
	}
}

func TestJoinVisitSeriesNames_JoinsAndToleratesMissing(t *testing.T) {
	rows := []visitSeriesRow{
		{EntityKey: "vtx.visitseries.A", PatientKey: "vtx.patient.alice", ProviderKey: "vtx.provider.sam"},
		{EntityKey: "vtx.visitseries.B", PatientKey: "vtx.patient.unknown", ProviderKey: "vtx.provider.unknown"},
	}
	patients := []patientRow{{PatientKey: "vtx.patient.alice", Name: "Alice Rivera"}}
	providers := []providerRow{{ProviderKey: "vtx.provider.sam", Name: "Dr. Sam Okafor", Specialty: "Cardiology"}}

	joined := joinVisitSeriesNames(rows, patients, providers)
	if joined[0].PatientName != "Alice Rivera" || joined[0].ProviderName != "Dr. Sam Okafor" || joined[0].ProviderSpecialty != "Cardiology" {
		t.Fatalf("expected joined names, got %+v", joined[0])
	}
	if joined[1].PatientName != "" || joined[1].ProviderName != "" {
		t.Fatalf("expected empty names for unmatched keys, got %+v", joined[1])
	}
}
