package main

import (
	"encoding/json"
	"net/http"
	"sort"

	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// instructorProjection is one row of the wellness-domain `wellnessInstructors`
// lens.
type instructorProjection struct {
	InstructorKey string `json:"instructorKey"`
	DisplayName   string `json:"displayName"`
	StudioKey     string `json:"studioKey"`
}

// instructorRow is the picker row the staff class-scheduling form renders.
type instructorRow struct {
	InstructorKey string `json:"instructorKey"`
	DisplayName   string `json:"displayName"`
	StudioKey     string `json:"studioKey"`
}

// computeInstructors decodes every wellnessInstructors row, sorted by display
// name. A row that fails to decode or carries no instructorKey (a tombstoned
// projection entry) is skipped.
func computeInstructors(keys []string, get kvGetter) []instructorRow {
	rows := make([]instructorRow, 0, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p instructorProjection
		if json.Unmarshal(raw, &p) != nil || p.InstructorKey == "" {
			continue
		}
		rows = append(rows, instructorRow(p))
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].DisplayName != rows[j].DisplayName {
			return rows[i].DisplayName < rows[j].DisplayName
		}
		return rows[i].InstructorKey < rows[j].InstructorKey
	})
	return rows
}

// handleInstructors implements GET /api/instructors — who teaches here,
// served from the wellnessInstructors lens (P5), for the staff scheduling
// form's "led by" picker. It is a SESSION-GATED read rather than part of the
// public schedule: an instructor's name reaches an anonymous visitor only
// attached to a class they actually lead (wellnessSessions' instructorName),
// never as a bare staff directory to enumerate.
func (s *server) handleInstructors(w http.ResponseWriter, r *http.Request) {
	if _, err := s.authenticateRead(r); err != nil {
		s.writeAuthError(w, err)
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := wellnessdomain.WellnessInstructorsBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is wellness-domain installed and the Refractor projecting?)")
		return
	}
	rows := computeInstructors(keys, s.kvGetter(ctx, bucket))
	s.writeJSON(w, http.StatusOK, map[string]any{"instructors": rows})
}
