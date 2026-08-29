package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
)

// weaverTargetsBucket is the shared cross-package Weaver convergence bucket
// every actorAggregate lens projects into (packages/lease-signing/lenses.go).
const weaverTargetsBucket = "weaver-targets"

// leaseApplicationKeyPrefix is the OutputKeyPattern prefix of the
// lease-signing `leaseApplicationComplete` convergence lens
// ("leaseApplicationComplete.{actorSuffix}", packages/lease-signing/lenses.go).
// It is read out of the shared weaver-targets read model — never Core KV
// (P5). Mirrors cmd/wellness-app/residents.go's identical precedent.
const leaseApplicationKeyPrefix = "leaseApplicationComplete."

// leaseApplicationProjection is the subset of the `leaseApplicationComplete`
// row this app needs: the applicant identity and whether the landlord has
// approved the lease (a resident-visit hint only — CreateAppointment
// re-derives the authoritative check itself from the leaseapp's own
// .tenancy aspect + applicationFor link, never trusting this projection as a
// gate).
type leaseApplicationProjection struct {
	EntityKey        string `json:"entityKey"`
	Applicant        string `json:"applicant"`
	LandlordApproved bool   `json:"landlordApproved"`
}

// residentRow is the resident/lease picker row the booking form uses to
// offer "book as a resident" — mirrors cmd/wellness-app/residents.go's
// identical picker, the established precedent for this cross-package join
// (Inc 5, the mixed-use composition clinic tail).
type residentRow struct {
	LeaseAppKey string `json:"leaseAppKey"`
	BookerKey   string `json:"bookerKey"`
	Approved    bool   `json:"approved"`
}

// computeResidents decodes every leaseApplicationComplete row, sorted by
// booker key for a stable lookup order. A row that fails to decode or
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

// handleResidents implements GET /api/residents — the booking form's
// resident-visit lookup, served from the shared leaseApplicationComplete
// convergence lens (P5). The FE matches a selected patient's own identityKey
// against a row's bookerKey to decide whether to attach leaseAppKey to
// CreateAppointment; a patient not tied to any lease books normally, with no
// residentVisit link (Inc 5, mixed-use composition design).
//
// Confined server-side (mirrors cmd/cafe-app's and cmd/wellness-app's
// resolveSubjectHats): the booking form's patient picker lets front-desk /
// operator staff select ANY patient, so staff see the full roster — the same
// "front-desk's view" grant patients.go's protected read describes — while
// everyone else (a bare patient session) sees only the row whose bookerKey
// is their own signed-in identity. There is no clinic-site topology on a
// lease application (it belongs to an unrelated LoftSpace building), so
// unlike café/wellness this has no further per-workplace narrowing: any
// clinic workplace anchor, or the operator role, is staff for this lookup.
func (s *server) handleResidents(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	hats, err := s.resolveSubjectHats(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
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
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, weaverTargetsBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	rows := computeResidents(keys, get)
	if hats.isOperator || hats.isFrontDesk() {
		s.writeJSON(w, http.StatusOK, map[string]any{"residents": rows})
		return
	}
	own := auth.IdentityKeyPrefix + hats.identityID
	filtered := make([]residentRow, 0, 1)
	for _, row := range rows {
		if row.BookerKey == own {
			filtered = append(filtered, row)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"residents": filtered})
}
