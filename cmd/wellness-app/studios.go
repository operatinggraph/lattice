package main

import (
	"encoding/json"
	"net/http"
	"sort"

	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// studioProjection is one row of the wellness-domain `wellnessStudios` lens.
type studioProjection struct {
	StudioKey string `json:"studioKey"`
	Name      string `json:"name"`
}

// studioRow is the studio-picker row the Schedule view renders.
type studioRow struct {
	StudioKey string `json:"studioKey"`
	Name      string `json:"name"`
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
		rows = append(rows, studioRow(p))
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
