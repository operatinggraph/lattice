package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
)

// housePolicyProjection is one row of the cafe-domain `cafeHousePolicies`
// lens (HousePoliciesBucket, P5) — a location carrying a .cafePolicy aspect
// and the self-service tab limit it records. TabLimitCents is a pointer so a
// row projected without the column (never re-projected under the current
// spec) is skipped rather than read as a $0 limit that would close
// self-service at that house. Name is the location's own .presentation
// display name, null when never set.
type housePolicyProjection struct {
	LocationKey   string   `json:"locationKey"`
	TabLimitCents *float64 `json:"tabLimitCents"`
	Name          string   `json:"name"`
}

// housePolicyRow is the API shape /api/house-policies returns: the location,
// its recorded limit and a display name for the desk's panel.
type housePolicyRow struct {
	LocationKey   string `json:"locationKey"`
	TabLimitCents int64  `json:"tabLimitCents"`
	Name          string `json:"name"`
}

// computeHousePolicies decodes every cafeHousePolicies row into a map keyed
// by location. A row that fails to decode, carries no locationKey, or
// carries no limit is skipped — absent = no limit recorded, the op's own
// reading (house_tab_limit, cafe-domain/ddls.go).
func computeHousePolicies(keys []string, get kvGetter) map[string]housePolicyRow {
	out := map[string]housePolicyRow{}
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p housePolicyProjection
		if json.Unmarshal(raw, &p) != nil || p.LocationKey == "" || p.TabLimitCents == nil || *p.TabLimitCents < 0 {
			continue
		}
		out[p.LocationKey] = housePolicyRow{LocationKey: p.LocationKey, TabLimitCents: int64(*p.TabLimitCents), Name: p.Name}
	}
	return out
}

// effectiveTabLimit composes a lease's house tab limit from its covering
// locations (the cafeLeaseWorkplaces column: the unit and every containedIn
// ancestor) and the recorded policies: the MINIMUM limit among the covering
// locations that record one, or (0, false) when none does. This is the same
// tightest-policy rule house_tab_limit applies on the resident-self leg of
// OpenTab / Charge (cafe-domain/ddls.go), so what the card shows is what the
// op refuses on.
func effectiveTabLimit(coveringLocations []string, policies map[string]housePolicyRow) (int64, bool) {
	var limit int64
	found := false
	for _, loc := range coveringLocations {
		p, ok := policies[strings.TrimSpace(loc)]
		if !ok {
			continue
		}
		if !found || p.TabLimitCents < limit {
			limit = p.TabLimitCents
			found = true
		}
	}
	return limit, found
}

// housePolicies lists and decodes the cafeHousePolicies bucket — the one KV
// fetch shared by the residents join (leaseHouseLimits) and /api/house-policies.
func (s *server) housePolicies(ctx context.Context) (map[string]housePolicyRow, error) {
	keys, err := s.conn.KVListKeys(ctx, cafedomain.HousePoliciesBucket)
	if err != nil {
		return nil, err
	}
	return computeHousePolicies(keys, s.kvGetter(ctx, cafedomain.HousePoliciesBucket)), nil
}

// leaseHouseLimits resolves every lease's effective house tab limit off the
// caller's already-fetched cafeLeaseWorkplaces rows joined to the policies — a lease whose chain
// records no policy is absent from the map, which handleResidents serializes
// as null (no limit), never as 0. A policies bucket that cannot be listed
// (the lens not yet projected on this stack) degrades the same way — every
// lease reads as no limit known — rather than failing the whole roster: the
// limit here is a courtesy the op still enforces, while the roster gates the
// resident's own Open Tab and the desk's picker, which a missing bucket must
// not take down (the loadBalances degrade-to-hidden posture, app.js).
// /api/house-policies itself still reports the listing error, so the desk's
// panel names the gap.
func (s *server) leaseHouseLimits(ctx context.Context, rows []leaseWorkplaceProjection) map[string]int64 {
	policies, err := s.housePolicies(ctx)
	if err != nil {
		policies = map[string]housePolicyRow{}
	}
	limits := map[string]int64{}
	for _, p := range rows {
		if limit, ok := effectiveTabLimit(p.CoveringLocations, policies); ok {
			limits[p.LeaseAppKey] = limit
		}
	}
	return limits
}

// handleHousePolicies implements GET /api/house-policies — the recorded
// house tab limits, served from the cafeHousePolicies lens (P5). A `worksAt`
// staffer sees every policy on the chains of the leases their workplace
// covers — their own house's row (the one SetCafePolicy edits from the
// panel) AND any policy above or below it that binds those residents, since
// the op applies the tightest on the chain and a panel showing only the
// workplace's own row would promise a rule the op does not apply; the
// operator sees every policy; a resident sees the policies covering their
// own lease(s), which is what their tab limit is composed from. A caller
// matching none of those sees an empty list, never an error.
func (s *server) handleHousePolicies(w http.ResponseWriter, r *http.Request) {
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

	keys, err := conn.KVListKeys(ctx, cafedomain.HousePoliciesBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+cafedomain.HousePoliciesBucket+": "+err.Error()+" (is cafe-domain 0.18.0 installed and the Refractor projecting?)")
		return
	}
	policies := computeHousePolicies(keys, s.kvGetter(ctx, cafedomain.HousePoliciesBucket))

	admit := map[string]bool{}
	switch {
	case hats.isOperator:
		for k := range policies {
			admit[k] = true
		}
	case hats.isStaff():
		for _, work := range hats.workplaces {
			admit[work] = true
		}
		rows, err := s.leaseWorkplaceRows(ctx)
		if err != nil {
			s.writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		for _, row := range rows {
			if !hats.covers(row.CoveringLocations) {
				continue
			}
			for _, loc := range row.CoveringLocations {
				if loc = strings.TrimSpace(loc); loc != "" {
					admit[loc] = true
				}
			}
		}
	default:
		own, err := s.residentOwnLeases(ctx, hats.identityID)
		if err != nil {
			s.writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		for leaseAppKey := range own {
			for loc := range s.leaseCoveringLocations(ctx, leaseAppKey) {
				admit[loc] = true
			}
		}
	}
	rows := make([]housePolicyRow, 0, len(policies))
	for k, p := range policies {
		if admit[k] {
			rows = append(rows, p)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].LocationKey < rows[j].LocationKey })
	s.writeJSON(w, http.StatusOK, map[string]any{"policies": rows})
}
