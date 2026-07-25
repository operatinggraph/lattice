package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
)

// leaseApplicationKeyPrefix is the OutputKeyPattern prefix of the
// lease-signing `leaseApplicationComplete` convergence lens
// ("leaseApplicationComplete.{actorSuffix}", packages/lease-signing/lenses.go).
// It is read out of the shared weaver-targets read model — never Core KV
// (P5). Mirrors cmd/wellness-app/residents.go's own decode of this lens,
// the established precedent for a vertical app resolving "which identity
// holds which lease" without a protected read model.
const leaseApplicationKeyPrefix = "leaseApplicationComplete."

// leaseApplicationProjection is the subset of the `leaseApplicationComplete`
// row this app needs: the applicant identity (the resident) and whether the
// landlord has approved the lease. OpenTab/Settle's own self-scope check
// re-derives the authoritative applicationFor link itself (packages/
// cafe-domain/ddls.go) — this projection is a picker convenience only.
type leaseApplicationProjection struct {
	EntityKey        string `json:"entityKey"`
	Applicant        string `json:"applicant"`
	LandlordApproved bool   `json:"landlordApproved"`
}

// residentRow is the resident/lease roster row the Resident view and the
// staff-only Front Desk render — the "who" dimension the POS/Front Desk
// lease picker (leases.go) has no notion of, since cafeLeaseAccounts is keyed
// by lease, not identity, and only carries a row once a tab has ever
// settled.
type residentRow struct {
	LeaseAppKey string `json:"leaseAppKey"`
	BookerKey   string `json:"bookerKey"`
	Approved    bool   `json:"approved"`
}

// computeResidents decodes every leaseApplicationComplete row, sorted by
// booker key for a stable picker order. A row that fails to decode or
// carries no applicant (a tombstoned projection entry, or one that hasn't
// reached the applicant-known stage yet) is skipped. Mirrors
// cmd/wellness-app/residents.go's computeResidents.
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

// handleResidents implements GET /api/residents — the lease-applicant
// roster, served from the shared leaseApplicationComplete convergence lens
// (P5). A `worksAt` staffer sees every applicant; a resident sees only their
// own row(s) (persona-worlds-design.md Fire W4 §3).
func (s *server) handleResidents(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	hats, err := s.resolveSubjectHats(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, err.Error())
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
	if !hats.isStaff {
		own := auth.IdentityKeyPrefix + hats.identityID
		filtered := make([]residentRow, 0, len(rows))
		for _, row := range rows {
			if row.BookerKey == own {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"residents": rows})
}

// residentOwnLeases returns the set of lease keys identityID (a bare
// identity NanoID) applied for — the leaseApplicationComplete rows whose
// applicant is this identity — used to scope every resident-visible
// endpoint to leases they hold (persona-worlds-design.md Fire W4 §3).
func (s *server) residentOwnLeases(ctx context.Context, identityID string) (map[string]bool, error) {
	keys, err := s.conn.KVListKeys(ctx, weaverTargetsBucket)
	if err != nil {
		return nil, err
	}
	rows := computeResidents(keys, s.kvGetter(ctx, weaverTargetsBucket))
	own := auth.IdentityKeyPrefix + identityID
	leases := map[string]bool{}
	for _, row := range rows {
		if row.BookerKey == own {
			leases[row.LeaseAppKey] = true
		}
	}
	return leases, nil
}
