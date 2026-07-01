package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/asolgan/lattice/internal/bootstrap"
	clinicdomain "github.com/asolgan/lattice/packages/clinic-domain"
)

// visitSeriesKeyPrefix is the OutputKeyPattern prefix of the clinic-reminders
// `visitSeriesDue` convergence lens (Contract #10 §10.2:
// "visitSeriesDue.{actorSuffix}"). It is read out of the shared weaver-targets
// read model — never Core KV (P5).
const visitSeriesKeyPrefix = "visitSeriesDue."

// visitSeriesRow is one projected `visitSeriesDue` row — the live rolling state
// of a recurring visit series (packages/clinic-reminders/visitseries.go). The
// JSON tags match the lens's BodyColumns verbatim (entityKey, nextDueAt,
// intervalDays, occurrenceCount, active, missing_series_advance, patientKey,
// providerKey) so decode needs no field renaming. PatientName / ProviderName /
// ProviderSpecialty are NOT lens columns — they are joined in server-side from
// the clinicPatients / clinicProviders lenses (the appointmentRow precedent).
type visitSeriesRow struct {
	EntityKey            string `json:"entityKey"`
	PatientKey           string `json:"patientKey"`
	PatientName          string `json:"patientName,omitempty"`
	ProviderKey          string `json:"providerKey"`
	ProviderName         string `json:"providerName,omitempty"`
	ProviderSpecialty    string `json:"providerSpecialty,omitempty"`
	IntervalDays         int    `json:"intervalDays"`
	NextDueAt            string `json:"nextDueAt"`
	OccurrenceCount      int    `json:"occurrenceCount"`
	Active               bool   `json:"active"`
	MissingSeriesAdvance bool   `json:"missing_series_advance"`
}

// computeVisitSeries assembles visit-series rows from the `visitSeriesDue`
// weaver-targets read model. It keeps only keys under the convergence prefix
// (the bucket is shared with other packages' targets) and decodes each row. A
// row that fails to decode or carries no entityKey (a tombstoned projection) is
// skipped. Rows sort by nextDueAt (then entityKey) so the due-soonest floats up.
func computeVisitSeries(keys []string, get kvGetter) []visitSeriesRow {
	rows := make([]visitSeriesRow, 0)
	for _, k := range keys {
		if !strings.HasPrefix(k, visitSeriesKeyPrefix) {
			continue
		}
		raw, ok := get(k)
		if !ok {
			continue
		}
		var row visitSeriesRow
		if json.Unmarshal(raw, &row) != nil || row.EntityKey == "" {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].NextDueAt != rows[j].NextDueAt {
			return rows[i].NextDueAt < rows[j].NextDueAt
		}
		return rows[i].EntityKey < rows[j].EntityKey
	})
	return rows
}

// joinVisitSeriesNames decorates each row with its patient/provider display
// name (and provider specialty), mirroring computeAppointments' neighbour-name
// join. A row whose patient or provider key has no matching roster entry keeps
// an empty name rather than failing the whole response.
func joinVisitSeriesNames(rows []visitSeriesRow, patients []patientRow, providers []providerRow) []visitSeriesRow {
	patientNames := make(map[string]string, len(patients))
	for _, p := range patients {
		patientNames[p.PatientKey] = p.Name
	}
	providerByKey := make(map[string]providerRow, len(providers))
	for _, p := range providers {
		providerByKey[p.ProviderKey] = p
	}
	for i := range rows {
		rows[i].PatientName = patientNames[rows[i].PatientKey]
		if pr, ok := providerByKey[rows[i].ProviderKey]; ok {
			rows[i].ProviderName = pr.Name
			rows[i].ProviderSpecialty = pr.Specialty
		}
	}
	return rows
}

// handleVisitSeries implements GET /api/visit-series — the clinic-wide "visit
// series due" worklist plus the per-patient series list the patient view's
// start/pause/resume controls read, served entirely from lens read models (P5:
// never Core KV): the `visitSeriesDue` weaver-target for the rolling deadline
// state, joined against `clinicPatients` / `clinicProviders` for display names.
func (s *server) handleVisitSeries(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	getter := func(bucket string) (kvGetter, []string, error) {
		keys, err := conn.KVListKeys(ctx, bucket)
		if err != nil {
			return nil, nil, err
		}
		get := func(key string) ([]byte, bool) {
			entry, err := conn.KVGet(ctx, bucket, key)
			if err != nil {
				return nil, false
			}
			return entry.Value, true
		}
		return get, keys, nil
	}

	seriesGet, seriesKeys, err := getter(bootstrap.WeaverTargetsBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bootstrap.WeaverTargetsBucket+": "+err.Error()+" (is clinic-reminders installed and the Refractor projecting?)")
		return
	}
	patientGet, patientKeys, err := getter(clinicdomain.ClinicPatientsBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+clinicdomain.ClinicPatientsBucket+": "+err.Error()+" (is clinic-domain installed and the Refractor projecting?)")
		return
	}
	providerGet, providerKeys, err := getter(clinicdomain.ClinicProvidersBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+clinicdomain.ClinicProvidersBucket+": "+err.Error()+" (is clinic-domain installed and the Refractor projecting?)")
		return
	}

	rows := computeVisitSeries(seriesKeys, seriesGet)
	rows = joinVisitSeriesNames(rows, computePatients(patientKeys, patientGet), computeProviders(providerKeys, providerGet))
	s.writeJSON(w, http.StatusOK, map[string]any{"series": rows, "count": len(rows)})
}
