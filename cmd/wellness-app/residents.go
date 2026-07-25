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

// residencyRow is one lease the signed-in member holds. Its only job is the
// resident-rate hint: CreateBooking takes an optional leaseAppKey and
// re-derives the authoritative check itself from that leaseapp's own .tenancy
// aspect + applicationFor link, so this is a lookup the booker performs about
// THEMSELVES, never a picker of other people.
type residencyRow struct {
	LeaseAppKey string `json:"leaseAppKey"`
	Approved    bool   `json:"approved"`
}

// computeOwnResidency decodes every leaseApplicationComplete row and keeps
// only those whose applicant is bookerKey — the caller's own leases, sorted
// for a stable answer. A row that fails to decode or carries no applicant (a
// tombstoned projection entry, or one that hasn't reached the
// applicant-known stage yet) is skipped.
func computeOwnResidency(keys []string, get kvGetter, bookerKey string) []residencyRow {
	rows := make([]residencyRow, 0)
	if bookerKey == "" {
		return rows
	}
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
		if p.Applicant != bookerKey {
			continue
		}
		rows = append(rows, residencyRow{LeaseAppKey: p.EntityKey, Approved: p.LandlordApproved})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].LeaseAppKey < rows[j].LeaseAppKey })
	return rows
}

// handleMyResidency implements GET /api/my-residency — the leases the
// SIGNED-IN member holds, served from the shared leaseApplicationComplete
// convergence lens (P5) and scoped server-side to the session's own subject.
// The FE needs it to pass CreateBooking's optional resident-rate hint; nobody
// needs, and nothing here returns, anyone else's residency.
func (s *server) handleMyResidency(w http.ResponseWriter, r *http.Request) {
	subject, err := s.authenticateRead(r)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
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
	rows := computeOwnResidency(keys, s.kvGetter(ctx, weaverTargetsBucket), auth.IdentityKeyPrefix+subject)
	s.writeJSON(w, http.StatusOK, map[string]any{"leases": rows})
}
