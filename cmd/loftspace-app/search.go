package main

import (
	"context"
	"net/http"
	"strings"
)

// Front-of-house unified search (search-target-adapter-design.md §0a): one
// staff/landlord search box fans out typed queries over the PROTECTED
// Postgres read models in a single RLS session, then enriches person hits
// with a keyed join rather than a graph walk. RLS does the authorization
// work for free — a landlord's search sees only their own units/applicants
// (read_landlord_lease_applications is landlord-anchored), a staff actor
// holding the roster's WildcardAnchor grant sees everything, and an
// ordinary actor sees nothing, all from the same query text.
//
// Units are matched against read_landlord_lease_applications rather than the
// applicant-facing availableListings lens: the lens is a public NATS-KV
// bucket (no RLS, no SQL search surface) covering only currently-listed
// inventory, whereas the staff/landlord surface this box serves needs the
// protected, RLS-scoped record of units-with-applications the design's own
// consumer shape describes ("protected read models"). The applicant Browse
// view keeps its existing client-side city/address filter unchanged.

// searchPersonHit is one "People" result row: an applicant found either via
// the roster name match or the landlord-scoped applicant-name match (see
// selectSearchApplicantMatchesSQL), enriched with a capped, ranked list of
// the RLS-visible applications against that applicant.
type searchPersonHit struct {
	IdentityKey  string                 `json:"identityKey"`
	Name         string                 `json:"name"`
	Applications []protectedLandlordRow `json:"applications"`
}

// searchResult is the GET /api/search response shape: grouped typed hits.
type searchResult struct {
	Query  string              `json:"query"`
	People []searchPersonHit   `json:"people"`
	Units  []landlordUnitGroup `json:"units"`
}

// maxApplicationsPerPersonHit caps the enrichment sub-rows per person hit.
// The people/unit hit counts themselves are capped in SQL (LIMIT 20 below).
const maxApplicationsPerPersonHit = 3

// selectSearchPeopleSQL matches the roster by case-insensitive name
// substring. No auth WHERE — RLS (WildcardAnchor-only, mirrors
// selectIdentitiesSQL) scopes the roster to actors holding the reserved
// staff grant; anyone else gets zero rows.
const selectSearchPeopleSQL = `
SELECT identity_key, name
FROM read_loftspace_identities
WHERE name IS NOT NULL AND name ILIKE $1
ORDER BY name, identity_key
LIMIT 20`

// selectSearchUnitMatchesSQL finds the distinct units whose address/city/
// region matched, capped before the enrichment join so a broad term cannot
// pull the whole managed portfolio into the response.
const selectSearchUnitMatchesSQL = `
SELECT DISTINCT unit_key
FROM read_landlord_lease_applications
WHERE unit_key IS NOT NULL
  AND (unit_address ILIKE $1 OR unit_city ILIKE $1 OR unit_region ILIKE $1)
LIMIT 20`

// selectSearchApplicantMatchesSQL finds applicants by the landlord-scoped
// Secure-Lens contact name (read_landlord_lease_applications.applicant_name)
// rather than the roster alone. The roster (read_loftspace_identities) is
// WildcardAnchor-only (staff), so a landlord actor — who legitimately has no
// roster grant — would otherwise get zero "People" hits for their own
// applicants; this is how a landlord's search finds "Alice" without needing
// staff-level roster access. RLS scopes the match to units the actor already
// manages (or every landlord's, for a wildcard-holding staff actor), so this
// adds no new disclosure beyond what the actor's own applications already show.
const selectSearchApplicantMatchesSQL = `
SELECT DISTINCT applicant
FROM read_landlord_lease_applications
WHERE applicant IS NOT NULL AND applicant_name ILIKE $1
LIMIT 20`

// selectLandlordRowsForApplicantsSQL and selectLandlordRowsForUnitsSQL reuse
// read_landlord_lease_applications' full column set (selectLandlordApplicationsSQL's
// SELECT list) narrowed by an applicant/unit-key set, ranked active/signed-first
// per the design's consumer shape (§0a).
const searchLandlordColumns = `entity_key, applicant, applicant_name, applicant_email, applicant_phone,
       landlord_key, unit_key, unit_address, unit_city,
       unit_region, unit_rent, unit_currency, unit_status, signed_at,
       landlord_decision, decline_reason, terms_move_in_date,
       terms_lease_term_months, terms_requested_rent,
       COALESCE(profile_submitted, false), income_to_rent_met, employment_verified,
       reference_count, has_co_applicant, has_guarantor,
       guarantor_income_to_rent_met, COALESCE(qualified, false)`

const selectLandlordRowsForApplicantsSQL = `
SELECT ` + searchLandlordColumns + `
FROM read_landlord_lease_applications
WHERE applicant = ANY($1)
ORDER BY applicant, (signed_at IS NOT NULL) DESC, (landlord_decision = 'approved') DESC, app_id`

const selectLandlordRowsForUnitsSQL = `
SELECT ` + searchLandlordColumns + `
FROM read_landlord_lease_applications
WHERE unit_key = ANY($1)
ORDER BY unit_key, app_id`

// scanLandlordRow scans one searchLandlordColumns row — the same column
// order as selectLandlordApplicationsSQL's Scan in queryLandlordApplications.
func scanLandlordRow(rows interface {
	Scan(dest ...any) error
}) (protectedLandlordRow, error) {
	var row protectedLandlordRow
	err := rows.Scan(
		&row.EntityKey, &row.Applicant,
		&row.ApplicantName, &row.ApplicantEmail, &row.ApplicantPhone,
		&row.LandlordKey, &row.UnitKey,
		&row.UnitAddress, &row.UnitCity, &row.UnitRegion, &row.UnitRent,
		&row.UnitCurrency, &row.UnitStatus, &row.SignedAt, &row.LandlordDecision,
		&row.DeclineReason, &row.TermsMoveInDate, &row.TermsLeaseTerm,
		&row.TermsRequestedRent,
		&row.ProfileSubmitted, &row.IncomeToRentMet, &row.EmploymentVerified,
		&row.ReferenceCount, &row.HasCoApplicant, &row.HasGuarantor,
		&row.GuarantorIncomeToRentMet, &row.Qualified,
	)
	if err != nil {
		return protectedLandlordRow{}, err
	}
	deriveLandlordRowFlags(&row)
	return row, nil
}

// querySearch runs the typed fan-out inside one per-request RLS transaction
// (one SET LOCAL lattice.actor_id, mirroring queryApplications' pooling-safety
// discipline): match people by name, match units by address/city/region, then
// enrich both with the same read_landlord_lease_applications join RLS already
// scopes to the actor.
func querySearch(ctx context.Context, pool pgxBeginner, actorID, q string) (searchResult, error) {
	result := searchResult{Query: q, People: []searchPersonHit{}, Units: []landlordUnitGroup{}}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return result, err
	}

	like := "%" + q + "%"

	// People: the roster (name match, wildcard/staff-only) UNION the
	// landlord-scoped applicant-name match (works for a landlord with no
	// roster grant, over their own units' applicants only). rosterNames
	// carries the canonical display name where the roster matched; an
	// applicant found only via the landlord table gets its name filled in
	// from applicant_name once the enrichment rows are scanned below.
	applicantOrder := make([]string, 0, 8)
	seenApplicant := make(map[string]bool, 8)
	rosterNames := make(map[string]string, 8)

	peopleRows, err := tx.Query(ctx, selectSearchPeopleSQL, like)
	if err != nil {
		return result, err
	}
	for peopleRows.Next() {
		var identityKey, name string
		if err := peopleRows.Scan(&identityKey, &name); err != nil {
			peopleRows.Close()
			return result, err
		}
		rosterNames[identityKey] = name
		if !seenApplicant[identityKey] {
			seenApplicant[identityKey] = true
			applicantOrder = append(applicantOrder, identityKey)
		}
	}
	if err := peopleRows.Err(); err != nil {
		peopleRows.Close()
		return result, err
	}
	peopleRows.Close()

	applicantMatchRows, err := tx.Query(ctx, selectSearchApplicantMatchesSQL, like)
	if err != nil {
		return result, err
	}
	for applicantMatchRows.Next() {
		var identityKey string
		if err := applicantMatchRows.Scan(&identityKey); err != nil {
			applicantMatchRows.Close()
			return result, err
		}
		if !seenApplicant[identityKey] {
			seenApplicant[identityKey] = true
			applicantOrder = append(applicantOrder, identityKey)
		}
	}
	if err := applicantMatchRows.Err(); err != nil {
		applicantMatchRows.Close()
		return result, err
	}
	applicantMatchRows.Close()

	if len(applicantOrder) > 0 {
		appRows, err := tx.Query(ctx, selectLandlordRowsForApplicantsSQL, applicantOrder)
		if err != nil {
			return result, err
		}
		byApplicant := make(map[string][]protectedLandlordRow)
		for appRows.Next() {
			row, err := scanLandlordRow(appRows)
			if err != nil {
				appRows.Close()
				return result, err
			}
			if _, hasRosterName := rosterNames[row.Applicant]; !hasRosterName && row.ApplicantName != nil {
				rosterNames[row.Applicant] = *row.ApplicantName
			}
			if len(byApplicant[row.Applicant]) < maxApplicationsPerPersonHit {
				byApplicant[row.Applicant] = append(byApplicant[row.Applicant], row)
			}
		}
		if err := appRows.Err(); err != nil {
			appRows.Close()
			return result, err
		}
		appRows.Close()

		for _, identityKey := range applicantOrder {
			result.People = append(result.People, searchPersonHit{
				IdentityKey:  identityKey,
				Name:         rosterNames[identityKey],
				Applications: byApplicant[identityKey],
			})
		}
	}

	// Units: match by address/city/region, then enrich via the same join.
	unitRows, err := tx.Query(ctx, selectSearchUnitMatchesSQL, like)
	if err != nil {
		return result, err
	}
	var unitKeys []string
	for unitRows.Next() {
		var k string
		if err := unitRows.Scan(&k); err != nil {
			unitRows.Close()
			return result, err
		}
		unitKeys = append(unitKeys, k)
	}
	if err := unitRows.Err(); err != nil {
		unitRows.Close()
		return result, err
	}
	unitRows.Close()

	if len(unitKeys) > 0 {
		rows, err := tx.Query(ctx, selectLandlordRowsForUnitsSQL, unitKeys)
		if err != nil {
			return result, err
		}
		var flat []protectedLandlordRow
		for rows.Next() {
			row, err := scanLandlordRow(rows)
			if err != nil {
				rows.Close()
				return result, err
			}
			flat = append(flat, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return result, err
		}
		rows.Close()
		result.Units = groupLandlordRowsByUnit(flat)
	}

	if err := tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}

// handleSearch implements GET /api/search?q=<term> — the front-of-house
// unified search. A blank/whitespace-only term returns an empty result
// without touching Postgres (mirrors "no query, no work").
func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway, "protected read model not configured")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		s.writeJSON(w, http.StatusOK, searchResult{Query: "", People: []searchPersonHit{}, Units: []landlordUnitGroup{}})
		return
	}

	ctx, cancel := s.reqContext(r)
	defer cancel()

	result, err := querySearch(ctx, s.pgPool, actor.Subject, q)
	if err != nil {
		s.logger.Error("unified search", "error", err)
		s.writeError(w, http.StatusBadGateway, "search failed")
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}
