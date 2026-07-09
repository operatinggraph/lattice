package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// weaverTargetsBucket is the shared cross-package Weaver convergence bucket
// every actorAggregate lens projects into (packages/lease-signing/lenses.go).
const weaverTargetsBucket = "weaver-targets"

// leaseApplicationKeyPrefix is the OutputKeyPattern prefix of the
// lease-signing `leaseApplicationComplete` convergence lens
// ("leaseApplicationComplete.{actorSuffix}", packages/lease-signing/lenses.go).
// It is read out of the shared weaver-targets read model — never Core KV
// (P5). Mirrors cmd/loftspace-app/applicationsource.go's applicationKeyPrefix
// decode, the established precedent for reading this lens.
const leaseApplicationKeyPrefix = "leaseApplicationComplete."

// leaseApplicationProjection is the subset of the `leaseApplicationComplete`
// row this app needs: the applicant identity (the booker) and whether the
// landlord has approved the lease (a resident-rate hint only — CreateBooking
// re-derives the authoritative check itself from the leaseapp's own
// .tenancy aspect + applicationFor link, never trusting this projection as a
// gate).
type leaseApplicationProjection struct {
	EntityKey        string `json:"entityKey"`
	Applicant        string `json:"applicant"`
	LandlordApproved bool   `json:"landlordApproved"`
}

// residentRow is the resident/lease picker row the Schedule + My Classes
// views render — the "who" dimension for a thin admin FE with no
// per-resident login, mirroring cmd/cafe-app's lease picker precedent.
type residentRow struct {
	LeaseAppKey string `json:"leaseAppKey"`
	BookerKey   string `json:"bookerKey"`
	Approved    bool   `json:"approved"`
}

// computeResidents decodes every leaseApplicationComplete row, sorted by
// booker key for a stable picker order. A row that fails to decode or
// carries no applicant (a tombstoned projection entry, or one that hasn't
// reached the applicant-known stage yet) is skipped.
func computeResidents(keys []string, get kvGetter) []residentRow {
	rows := make([]residentRow, 0)
	for _, k := range keys {
		if !strings.HasPrefix(k, leaseApplicationKeyPrefix) {
			continue
		}
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p leaseApplicationProjection
		if json.Unmarshal(raw, &p) != nil || p.Applicant == "" || p.EntityKey == "" {
			continue
		}
		rows = append(rows, residentRow{LeaseAppKey: p.EntityKey, BookerKey: p.Applicant, Approved: p.LandlordApproved})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].BookerKey != rows[j].BookerKey {
			return rows[i].BookerKey < rows[j].BookerKey
		}
		return rows[i].LeaseAppKey < rows[j].LeaseAppKey
	})
	return rows
}

// handleResidents implements GET /api/residents — every lease applicant the
// Schedule (book) and My Classes (whose classes) pickers offer, served from
// the shared leaseApplicationComplete convergence lens (P5). A booker not
// tied to any lease can still be booked directly against a bookerKey typed
// into the FE — this picker is a convenience, not an enforcement boundary
// (wellness classes are not lease-gated, per wellness-vertical-design.md).
func (s *server) handleResidents(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, weaverTargetsBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+weaverTargetsBucket+": "+err.Error()+" (is lease-signing installed and the Weaver projecting?)")
		return
	}
	rows := computeResidents(keys, s.kvGetter(ctx, weaverTargetsBucket))
	s.writeJSON(w, http.StatusOK, map[string]any{"residents": rows})
}
