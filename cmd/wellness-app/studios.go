package main

import (
	"encoding/json"
	"net/http"
	"sort"

	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// studioProjection is one row of the wellness-domain `wellnessStudios` lens.
// NoShowFeeCents mirrors sessionProjection's own ResidentPriceCents field
// (sessions.go): a pointer because the lens's cypher hands back a JSON
// number for a recorded policy and a JSON null for a studio with none, and
// that "policy 0 (fee-free)" vs "no policy recorded (SetBookingAttendance
// bills its 2500 default)" distinction is exactly what the roster's No-show
// button and the studio card's fee line both need preserved — collapsing it
// to a bare int64 would make an unset policy indistinguishable from a
// deliberate $0 one.
type studioProjection struct {
	StudioKey      string   `json:"studioKey"`
	Name           string   `json:"name"`
	NoShowFeeCents *float64 `json:"noShowFeeCents"`
}

// studioRow is the studio-picker row the Schedule view renders, and the row
// the Studios admin card and the roster's attendanceActions read the
// no-show policy off. NoShowFeeCents mirrors studioProjection's own field —
// nil (omitted from the JSON response) when the studio declares no policy.
type studioRow struct {
	StudioKey      string `json:"studioKey"`
	Name           string `json:"name"`
	NoShowFeeCents *int64 `json:"noShowFeeCents,omitempty"`
}

// computeStudios decodes every wellnessStudios row, sorted by name. A row
// that fails to decode or carries no studioKey (a tombstoned projection
// entry) is skipped.
func computeStudios(keys []string, get kvGetter) []studioRow {
	rows := make([]studioRow, 0, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p studioProjection
		if json.Unmarshal(raw, &p) != nil || p.StudioKey == "" {
			continue
		}
		var noShowFeeCents *int64
		if p.NoShowFeeCents != nil {
			v := int64(*p.NoShowFeeCents)
			noShowFeeCents = &v
		}
		rows = append(rows, studioRow{
			StudioKey:      p.StudioKey,
			Name:           p.Name,
			NoShowFeeCents: noShowFeeCents,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].StudioKey < rows[j].StudioKey
	})
	return rows
}

// handleStudios implements GET /api/studios — every studio the Schedule
// view's picker offers, served from the wellnessStudios lens (P5).
func (s *server) handleStudios(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := wellnessdomain.WellnessStudiosBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is wellness-domain installed and the Refractor projecting?)")
		return
	}
	rows := computeStudios(keys, s.kvGetter(ctx, bucket))
	s.writeJSON(w, http.StatusOK, map[string]any{"studios": rows})
}
