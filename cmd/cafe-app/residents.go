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
// settled. LeaseEnd and EndedAt carry no `omitempty`: the resident's own
// self-service Open Tab (cmd/cafe-app/web/app.js's residentOpenTabAllowed,
// via tenancyEnded) gates on them, and an omitted key would read identically
// to a lease with no projected term. EndedAt is the recorded FACT the
// tenancy ended (an early move-out via GiveNotice, or the term simply
// running out); LeaseEnd stays the term's nominal end for a lease with no
// EndedAt recorded yet.
type residentRow struct {
	LeaseAppKey string `json:"leaseAppKey"`
	BookerKey   string `json:"bookerKey"`
	Approved    bool   `json:"approved"`
	LeaseEnd    string `json:"leaseEnd"`
	EndedAt     string `json:"endedAt"`
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

// tenancyEnd carries one lease's projected term end (leaseEnd) and its
// recorded early-or-actual end (endedAt) — the two dates leaseTenancyEnds
// resolves per lease, either of which may be empty.
type tenancyEnd struct {
	LeaseEnd string
	EndedAt  string
}

// leaseTenancyEnds resolves every leaseAppKey's own tenancy end off the
// cafe-domain cafeLeaseWorkplaces lens (leaseWorkplaceRows, readauth.go) — a
// row with no projected term (or one this pass never read at all) is simply
// absent from the map, which callers read as the zero tenancyEnd (no term),
// never as an error condition of its own.
func (s *server) leaseTenancyEnds(ctx context.Context) (map[string]tenancyEnd, error) {
	rows, err := s.leaseWorkplaceRows(ctx)
	if err != nil {
		return nil, err
	}
	ends := make(map[string]tenancyEnd, len(rows))
	for _, p := range rows {
		if p.LeaseEnd != "" || p.EndedAt != "" {
			ends[p.LeaseAppKey] = tenancyEnd{LeaseEnd: p.LeaseEnd, EndedAt: p.EndedAt}
		}
	}
	return ends, nil
}

// handleResidents implements GET /api/residents — the lease-applicant
// roster, served from the shared leaseApplicationComplete convergence lens
// (P5). A `worksAt` staffer sees the applicants of the leases their workplace
// covers (facet-staff-worlds-design.md §9); a resident sees only their own
// row(s) (persona-worlds-design.md Fire W4 §3). Confining by LEASE also
// answers the resident case exactly: a resident's own leases are precisely the
// rows whose applicant is them, so both hats ask visibleLeases the same
// question and this roster needs no second rule of its own.
//
// Each row also carries leaseEnd and endedAt, joined from cafe-domain's
// cafeLeaseWorkplaces lens by leaseAppKey (leaseTenancyEnds) — the
// resident-readable half of the TenancyEnded fact OpenTab refuses on:
// endedAt is the recorded FACT the tenancy ended (an early move-out via
// GiveNotice, or the term simply running out), leaseEnd the term's nominal
// end for a lease with no endedAt recorded yet. The resident's own
// self-service Open Tab reads THIS endpoint; the staff-only
// /api/frontdesk-lease-details carries the same facts for the POS picker.
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
	visible, err := s.visibleLeases(ctx, hats)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	leaseEnds, err := s.leaseTenancyEnds(ctx)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	filtered := make([]residentRow, 0, len(rows))
	for _, row := range rows {
		if !visible.admits(row.LeaseAppKey) {
			continue
		}
		end := leaseEnds[row.LeaseAppKey]
		row.LeaseEnd = end.LeaseEnd
		row.EndedAt = end.EndedAt
		filtered = append(filtered, row)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"residents": filtered})
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
