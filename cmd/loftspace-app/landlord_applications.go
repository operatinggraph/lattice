package main

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// The landlord read boundary (D1.3 Increment 3) — the landlord/property-manager
// "applications to my units" view served from the PROTECTED
// read_landlord_lease_applications Postgres model as an AUTHENTICATED actor. The
// sibling of handleApplications (the applicant Fire-3 reader): identical
// verified-JWT → per-request txn → SET LOCAL lattice.actor_id → RLS path, but the
// protected model anchors each row on the MANAGING LANDLORD, so RLS returns only
// the applications to units the signed-in landlord manages.
//
// There is NO client-side scope filter and NO cap-read.residence grant lens: the
// row's authz_anchor IS the managing landlord's NanoID, and the primordial
// cap-read self-grant already grants every identity its own NanoID, so the §6.14
// set-membership policy scopes a landlord to exactly their units' applications. A
// non-landlord identity (no manages link → no row anchored to it) sees nothing.
//
// Like the applicant protected model, this carries the application DISPLAY scalars
// but NOT the Weaver-internal convergence aggregate (the gap/qualification
// booleans) — that §10.2 state is the rich operator console's domain
// (/api/unit-applications, the trusted-tool view) and D1.5 rolls it onto a
// protected model later. The status here is coarse, derived from landlord_decision
// + signed_at.

// protectedLandlordRow is one row of read_landlord_lease_applications, as scanned
// from the RLS-scoped read. RLS has already restricted the rows to the requesting
// landlord, so there is no client-side filter. Nullable columns (the unit/terms/
// signature/decision scalars) are pointers so an absent value stays absent.
//
// The 7 profile-signal fields (D1.5 Rec C, decision-surface-design.md §5 Increment
// 2) are the SAME informational qualification signals the trusted console's
// convergence lens projects — income/employment/references/co-applicant/guarantor
// — carried here so the RLS view can render `renderQualification` against a
// protected row too. ProfileSubmitted is a plain bool (the lens always projects it,
// even false); the rest are pointers because "no profile yet" projects them null
// (unknown), matching the console's null-vs-false distinction the FE already reads.
// ApplicantName / ApplicantEmail / ApplicantPhone are the Secure-Lens contact
// columns (Contract #3 §3.10): the lens decrypts the applicant identity's
// sensitive contact aspects into this RLS-protected table only, so the managing
// landlord — and nobody else — reads them here. Null when the applicant never
// recorded the aspect or has been crypto-shredded; the FE degrades to the bare
// key it already renders. Qualified (D1.5 Rec-C remainder) is the readiness
// clone — ssn on file, a fresh completed background check, a completed
// payment, and a signed lease — a plain bool the lens always projects (never
// null), mirroring the trusted console's applicantApproved.
type protectedLandlordRow struct {
	EntityKey          string   `json:"entityKey"`
	Applicant          string   `json:"applicant"`
	ApplicantName      *string  `json:"applicantName"`
	ApplicantEmail     *string  `json:"applicantEmail"`
	ApplicantPhone     *string  `json:"applicantPhone"`
	LandlordKey        string   `json:"landlordKey"`
	UnitKey            *string  `json:"unitKey"`
	UnitAddress        *string  `json:"unitAddress"`
	UnitCity           *string  `json:"unitCity"`
	UnitRegion         *string  `json:"unitRegion"`
	UnitRent           *float64 `json:"unitRent"`
	UnitCurrency       *string  `json:"unitCurrency"`
	UnitStatus         *string  `json:"unitStatus"`
	SignedAt           *string  `json:"signedAt"`
	LandlordDecision   *string  `json:"landlordDecision"`
	LandlordApproved   bool     `json:"landlordApproved"`
	LandlordDeclined   bool     `json:"landlordDeclined"`
	Declined           bool     `json:"declined"`
	DeclineReason      *string  `json:"declineReason"`
	TermsMoveInDate    *string  `json:"termsMoveInDate"`
	TermsLeaseTerm     *float64 `json:"termsLeaseTermMonths"`
	TermsRequestedRent *float64 `json:"termsRequestedRent"`
	// The anchored executed-lease artifact's pointers — the SAME columns the
	// applicant model carries (cmd/loftspace-app's GET /api/lease-document falls
	// back to this landlord-scoped row when the applicant-scoped read finds
	// none, so the managing landlord streams the document off their own row too).
	DocStoreName             *string  `json:"docStoreName"`
	DocFilename              *string  `json:"docFilename"`
	DocContentType           *string  `json:"docContentType"`
	ProfileSubmitted         bool     `json:"profileSubmitted"`
	IncomeToRentMet          *bool    `json:"incomeToRentMet"`
	EmploymentVerified       *bool    `json:"employmentVerified"`
	ReferenceCount           *float64 `json:"referenceCount"`
	HasCoApplicant           *bool    `json:"hasCoApplicant"`
	HasGuarantor             *bool    `json:"hasGuarantor"`
	GuarantorIncomeToRentMet *bool    `json:"guarantorIncomeToRentMet"`
	Qualified                bool     `json:"qualified"`
}

// landlordUnitGroup is the per-unit grouping the FE renders: a unit the signed-in
// landlord manages plus the RLS-scoped applications against it. Assembled
// server-side from the flat protected rows (one per application) so the FE keys on
// the same by-unit shape it already uses, minus the rich qualification profile the
// protected model does not carry.
type landlordUnitGroup struct {
	UnitKey      string                 `json:"unitKey"`
	UnitAddress  string                 `json:"unitAddress"`
	UnitRent     *float64               `json:"unitRent"`
	UnitStatus   string                 `json:"unitStatus"`
	Applications []protectedLandlordRow `json:"applications"`
}

// selectLandlordApplicationsSQL reads the protected landlord model. It carries NO
// auth WHERE — the RLS policy (FORCE ROW LEVEL SECURITY + the set-membership
// policy) injects the landlord scope from the txn-local lattice.actor_id session
// variable. Rows sort by (unit_key, app_id) for a stable grouped view.
const selectLandlordApplicationsSQL = `
SELECT entity_key, applicant, applicant_name, applicant_email, applicant_phone,
       landlord_key, unit_key, unit_address, unit_city,
       unit_region, unit_rent, unit_currency, unit_status, signed_at,
       landlord_decision, decline_reason, terms_move_in_date,
       terms_lease_term_months, terms_requested_rent,
       doc_store_name, doc_filename, doc_content_type,
       COALESCE(profile_submitted, false), income_to_rent_met, employment_verified,
       reference_count, has_co_applicant, has_guarantor,
       guarantor_income_to_rent_met, COALESCE(qualified, false)
FROM read_landlord_lease_applications
ORDER BY unit_key, app_id`

// queryLandlordApplications runs the protected landlord read inside a per-request
// transaction with a txn-local actor session variable — the same pooling-safety
// pattern as queryApplications: set_config(..., is_local=true) is discarded at
// COMMIT so the pooled connection returns clean (no actor → RLS deny-all) for the
// next request. actorID must be the bare landlord NanoID (VerifiedActor.Subject),
// matching actor_id in actor_read_grants and the §6.14 anchor representation.
func queryLandlordApplications(ctx context.Context, pool pgxBeginner, actorID string) ([]protectedLandlordRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, selectLandlordApplicationsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]protectedLandlordRow, 0)
	seenEntityKey := make(map[string]bool)
	for rows.Next() {
		var row protectedLandlordRow
		if err := rows.Scan(
			&row.EntityKey, &row.Applicant,
			&row.ApplicantName, &row.ApplicantEmail, &row.ApplicantPhone,
			&row.LandlordKey, &row.UnitKey,
			&row.UnitAddress, &row.UnitCity, &row.UnitRegion, &row.UnitRent,
			&row.UnitCurrency, &row.UnitStatus, &row.SignedAt, &row.LandlordDecision,
			&row.DeclineReason, &row.TermsMoveInDate, &row.TermsLeaseTerm,
			&row.TermsRequestedRent,
			&row.DocStoreName, &row.DocFilename, &row.DocContentType,
			&row.ProfileSubmitted, &row.IncomeToRentMet, &row.EmploymentVerified,
			&row.ReferenceCount, &row.HasCoApplicant, &row.HasGuarantor,
			&row.GuarantorIncomeToRentMet, &row.Qualified,
		); err != nil {
			return nil, err
		}
		// The lens projects one row per co-managing landlord on the same unit, each
		// carrying the SAME building/unit authz anchor alongside its own landlord's —
		// so a building-anchored reader's RLS grant matches every co-manager's row for
		// the one application (verticals.md: "12 Riverside Walk renders leaseapp …7×").
		// All facets but LandlordKey are identical across those rows (same application,
		// same unit); keep the first and drop the rest rather than rendering the same
		// application once per co-manager.
		if seenEntityKey[row.EntityKey] {
			continue
		}
		seenEntityKey[row.EntityKey] = true
		deriveLandlordRowFlags(&row)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// selectLandlordApplicationByKeySQL is selectLandlordApplicationsSQL narrowed
// to one entity_key (the lease-document GET's landlord-scope fallback, D1.3/
// D1.5): the applicant-scoped read finds no row for a key that isn't the
// caller's own application, and this lets the SAME actor read it off their
// landlord-anchored row instead. RLS still governs visibility: a key that
// exists but isn't among the caller's authz_anchors returns zero rows,
// identical to a genuinely absent key. ORDER BY + LIMIT 1 pick a stable row
// when a building-wide grant matches more than one co-manager's row for the
// same application (every facet but landlord_key is identical across them).
const selectLandlordApplicationByKeySQL = `
SELECT entity_key, applicant, applicant_name, applicant_email, applicant_phone,
       landlord_key, unit_key, unit_address, unit_city,
       unit_region, unit_rent, unit_currency, unit_status, signed_at,
       landlord_decision, decline_reason, terms_move_in_date,
       terms_lease_term_months, terms_requested_rent,
       doc_store_name, doc_filename, doc_content_type,
       COALESCE(profile_submitted, false), income_to_rent_met, employment_verified,
       reference_count, has_co_applicant, has_guarantor,
       guarantor_income_to_rent_met, COALESCE(qualified, false)
FROM read_landlord_lease_applications
WHERE entity_key = $1
ORDER BY landlord_id
LIMIT 1`

// queryLandlordApplicationByKey is queryLandlordApplications narrowed to one
// application, the landlord-scope counterpart of queryApplicationByKey. Same
// txn-local actor + pooling-safety discipline; returns ok=false (no error)
// when RLS or a genuinely missing key yields no row.
func queryLandlordApplicationByKey(ctx context.Context, pool pgxBeginner, actorID, entityKey string) (protectedLandlordRow, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return protectedLandlordRow{}, false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return protectedLandlordRow{}, false, err
	}

	var row protectedLandlordRow
	err = tx.QueryRow(ctx, selectLandlordApplicationByKeySQL, entityKey).Scan(
		&row.EntityKey, &row.Applicant,
		&row.ApplicantName, &row.ApplicantEmail, &row.ApplicantPhone,
		&row.LandlordKey, &row.UnitKey,
		&row.UnitAddress, &row.UnitCity, &row.UnitRegion, &row.UnitRent,
		&row.UnitCurrency, &row.UnitStatus, &row.SignedAt, &row.LandlordDecision,
		&row.DeclineReason, &row.TermsMoveInDate, &row.TermsLeaseTerm,
		&row.TermsRequestedRent,
		&row.DocStoreName, &row.DocFilename, &row.DocContentType,
		&row.ProfileSubmitted, &row.IncomeToRentMet, &row.EmploymentVerified,
		&row.ReferenceCount, &row.HasCoApplicant, &row.HasGuarantor,
		&row.GuarantorIncomeToRentMet, &row.Qualified,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return protectedLandlordRow{}, false, tx.Commit(ctx)
		}
		return protectedLandlordRow{}, false, err
	}
	deriveLandlordRowFlags(&row)
	if err := tx.Commit(ctx); err != nil {
		return protectedLandlordRow{}, false, err
	}
	return row, true, nil
}

// deriveLandlordRowFlags recomputes the landlord-decision booleans from the raw
// landlord_decision string (the protected model projects the raw value, not the
// booleans the Weaver convergence lens derived).
func deriveLandlordRowFlags(row *protectedLandlordRow) {
	if row.LandlordDecision == nil {
		return
	}
	switch *row.LandlordDecision {
	case "approved":
		row.LandlordApproved = true
	case "declined":
		row.LandlordDeclined = true
		row.Declined = true
	}
}

// groupLandlordRowsByUnit assembles the per-unit view from the flat RLS-scoped
// rows. Every row already belongs to a unit the signed-in landlord manages (RLS
// guaranteed it), so this is a pure presentation regroup — no auth logic. Units
// and their applications are stably ordered (the SQL already sorts; this preserves
// it). A row whose unit_key is null (a malformed projection) is skipped — every
// leaseapp has a unit (required at create), so a null unit_key has no place in a
// by-unit view.
func groupLandlordRowsByUnit(rows []protectedLandlordRow) []landlordUnitGroup {
	groups := make(map[string]*landlordUnitGroup)
	order := make([]string, 0)
	for _, r := range rows {
		if r.UnitKey == nil || *r.UnitKey == "" {
			continue
		}
		uk := *r.UnitKey
		g, ok := groups[uk]
		if !ok {
			g = &landlordUnitGroup{UnitKey: uk, Applications: []protectedLandlordRow{}}
			groups[uk] = g
			order = append(order, uk)
		}
		// Fill the unit facets from ANY row that carries them, not only the first.
		// Every row for a unit projects identical facets (same unit vertex), so this
		// is a no-op in the normal case — but it keeps the card populated if the
		// first row to arrive happened to have a null facet (e.g. an application
		// created before the unit's address was set).
		if g.UnitAddress == "" && r.UnitAddress != nil {
			g.UnitAddress = *r.UnitAddress
		}
		if g.UnitStatus == "" && r.UnitStatus != nil {
			g.UnitStatus = *r.UnitStatus
		}
		if g.UnitRent == nil && r.UnitRent != nil {
			g.UnitRent = r.UnitRent
		}
		g.Applications = append(g.Applications, r)
	}
	out := make([]landlordUnitGroup, 0, len(order))
	for _, uk := range order {
		out = append(out, *groups[uk])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UnitKey < out[j].UnitKey })
	return out
}

// handleLandlordApplications implements GET /api/landlord/applications — the
// landlord "applications to my units" view, served from the PROTECTED
// read_landlord_lease_applications model as an AUTHENTICATED actor (D1.3
// Increment 3, the landlord enforcement turn-on). The landlord identity comes
// ONLY from the verified JWT; RLS returns only the applications to units that
// landlord manages, so there is no client-supplied scope (a query param does
// nothing — RLS keys off the verified session var). A non-landlord actor sees an
// empty result, never a 403-vs-404 oracle.
func (s *server) handleLandlordApplications(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		// Log the deployment detail; return a generic message so a signed-in caller
		// learns no env-var names or topology from the boundary.
		s.logger.Error("landlord protected read requested but pgPool is nil (set LOFTSPACE_APP_PG_DSN + ensure Postgres and the lease-signing protected lens are up)")
		s.writeError(w, http.StatusBadGateway, "protected read model unavailable")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	rows, err := queryLandlordApplications(ctx, s.pgPool, actor.Subject)
	if err != nil {
		// Log the detail (which can carry the failing SQL / schema names) and return
		// a generic message — never echo a raw DB error to the client.
		s.logger.Error("read protected landlord lease applications", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected landlord lease-applications model")
		return
	}
	units := groupLandlordRowsByUnit(rows)
	resp := s.withProjectionHealth(ctx, pkgmgr.LensID("lease-signing", "landlordLeaseApplicationsRead"),
		map[string]any{"units": units, "count": len(units), "applicationCount": len(rows), "scope": "rls"})
	s.writeJSON(w, http.StatusOK, resp)
}
