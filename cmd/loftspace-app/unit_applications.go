package main

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	loftspacedomain "github.com/operatinggraph/lattice/packages/loftspace-domain"
)

// applicantSummary is one applicant's standing against a unit, as the landlord
// surface renders it: the application key, the applicant identity + its human
// name, and a coarse disposition derived from the convergence row. status is
// "leased" (landlord-approved AND the unit leased — the terminal done state),
// "approved" (the landlord approved, lease in flight), "qualified" (every applicant
// gap closed but the landlord has not decided), "declined" (a standing business
// rejection OR a landlord decline), or "in_review" (still converging). signed
// reflects whether the applicant has executed the lease (the .signature aspect).
// This console is informational only; the RLS-enforced protectedLandlordRow
// carries the equivalent qualified/landlordApproved/landlordDeclined columns
// the FE drives the Approve/Decline buttons off (landlord_applications.go).
type applicantSummary struct {
	LeaseAppKey      string `json:"leaseAppKey"`
	Applicant        string `json:"applicant"`
	ApplicantName    string `json:"applicantName"`
	Status           string `json:"status"`
	Signed           bool   `json:"signed"`
	Approved         bool   `json:"approved"`
	Declined         bool   `json:"declined"`
	Qualified        bool   `json:"qualified"`
	LandlordApproved bool   `json:"landlordApproved"`
	LandlordDeclined bool   `json:"landlordDeclined"`
	DeclineReason    string `json:"declineReason"`
	// The applicant's qualification profile — the DERIVED signals the landlord
	// reads to decide (never the raw financials). Pointers stay null until the
	// applicant submits a profile, so the FE renders "no profile yet" rather than
	// a misleading "income does not meet 3× rent".
	ProfileSubmitted   bool  `json:"profileSubmitted"`
	IncomeToRentMet    *bool `json:"incomeToRentMet"`
	EmploymentVerified *bool `json:"employmentVerified"`
	ReferenceCount     *int  `json:"referenceCount"`
	HasCoApplicant     *bool `json:"hasCoApplicant"`
	HasGuarantor       *bool `json:"hasGuarantor"`
	// guarantorIncomeToRentMet — does the guarantor's own income cover 3× rent.
	// Null until a guarantor income is supplied; lets the landlord lean on a
	// guarantor for a below-income applicant rather than a bare "+ Guarantor".
	GuarantorIncomeToRentMet *bool `json:"guarantorIncomeToRentMet"`
}

// unitApplicationsRow is the landlord's per-unit aggregate: the listed unit's
// identifying facets plus every live application against it. A unit with a live
// listing but no applications yet still appears (Applications empty,
// ApplicationCount 0) so the landlord sees their whole inventory, not only the
// units someone has applied to. unitRent is a pointer so an absent rent stays
// absent rather than rendering a misleading 0.
type unitApplicationsRow struct {
	UnitKey          string             `json:"unitKey"`
	UnitAddress      string             `json:"unitAddress"`
	UnitRent         *float64           `json:"unitRent"`
	UnitStatus       string             `json:"unitStatus"`
	ApplicationCount int                `json:"applicationCount"`
	Applications     []applicantSummary `json:"applications"`
	// Listing / Address are the full nested economics + address (the same shapes
	// the applicant Browse renders) so the landlord's Edit-listing form pre-fills
	// every field, not just the summary facets above. Null when the unit has no
	// listing projection (e.g. an application whose unit lost its listing).
	Listing json.RawMessage `json:"listing,omitempty"`
	Address json.RawMessage `json:"address,omitempty"`
}

// applicationStatus reduces a convergence row to the landlord's coarse
// disposition. declined wins (a standing verification rejection OR a landlord
// decline — the safest signal to surface). Then the landlord-decision states:
// landlord-approved + unit leased is the terminal "leased"; landlord-approved with
// the lease still in flight is "approved"; a qualified-but-undecided application
// (applicantApproved, no decision) is "qualified" — the state the landlord acts on
// (Approve/Decline). Everything else is still converging ("in_review").
func applicationStatus(a applicationRow) string {
	switch {
	case a.Declined:
		return "declined"
	case a.LandlordApproved && a.UnitStatus == "leased":
		return "leased"
	case a.LandlordApproved:
		return "approved"
	case a.ApplicantApproved:
		return "qualified"
	default:
		return "in_review"
	}
}

// groupByUnit assembles the landlord by-unit view from the three P5 read models
// already shipped: the `leaseApplicationComplete` convergence rows (one per live
// application, carrying unitKey + applicant + the gap/approval/declined state),
// the roster identities (identity key → human name, rosterIdentities), and the
// `availableListings` rows (every listed unit, so a unit with zero applications
// still shows). Units are keyed by unitKey; a listing seeds the unit's facets,
// and an application overrides them when it carries its own (a unit that has
// applications but has since lost its listing still shows via the row). An
// application with no unitKey is skipped — every leaseapp has a unit (required
// at create), so an empty unitKey marks a malformed/bare row with no place in a
// by-unit view.
func groupByUnit(apps []applicationRow, identities []identityView, listings []listingProjection) []unitApplicationsRow {
	names := make(map[string]string, len(identities))
	for _, id := range identities {
		names[id.Key] = id.Name
	}

	units := make(map[string]*unitApplicationsRow)
	ensure := func(key string) *unitApplicationsRow {
		u, ok := units[key]
		if !ok {
			u = &unitApplicationsRow{UnitKey: key, Applications: []applicantSummary{}}
			units[key] = u
		}
		return u
	}

	// Seed from listings so every listed unit appears, even with no applicants.
	// The full nested listing/address (via toRow) feed the landlord's Edit form.
	for _, l := range listings {
		u := ensure(l.UnitKey)
		u.UnitStatus = l.Status
		u.UnitRent = l.RentAmount
		u.UnitAddress = l.AddrLine1
		row := l.toRow()
		u.Listing = row.Listing
		u.Address = row.Address
	}
	// An application fills a unit the listing did not seed (lost its listing), or
	// confirms the same facets it reads from the same .listing/.address aspects.
	for _, a := range apps {
		if a.UnitKey == "" {
			continue
		}
		u := ensure(a.UnitKey)
		if a.UnitAddress != "" {
			u.UnitAddress = a.UnitAddress
		}
		if a.UnitRent != nil {
			u.UnitRent = a.UnitRent
		}
		if a.UnitStatus != "" {
			u.UnitStatus = a.UnitStatus
		}
		u.Applications = append(u.Applications, applicantSummary{
			LeaseAppKey:              a.EntityKey,
			Applicant:                a.Applicant,
			ApplicantName:            names[a.Applicant],
			Status:                   applicationStatus(a),
			Signed:                   !a.MissingSignature,
			Approved:                 a.ApplicantApproved,
			Declined:                 a.Declined,
			Qualified:                a.ApplicantApproved && !a.LandlordApproved && !a.LandlordDeclined,
			LandlordApproved:         a.LandlordApproved,
			LandlordDeclined:         a.LandlordDeclined,
			DeclineReason:            a.DeclineReason,
			ProfileSubmitted:         a.ProfileSubmitted,
			IncomeToRentMet:          a.IncomeToRentMet,
			EmploymentVerified:       a.EmploymentVerified,
			ReferenceCount:           a.ReferenceCount,
			HasCoApplicant:           a.HasCoApplicant,
			HasGuarantor:             a.HasGuarantor,
			GuarantorIncomeToRentMet: a.GuarantorIncomeToRentMet,
		})
	}

	rows := make([]unitApplicationsRow, 0, len(units))
	for _, u := range units {
		sort.Slice(u.Applications, func(i, j int) bool {
			return u.Applications[i].LeaseAppKey < u.Applications[j].LeaseAppKey
		})
		u.ApplicationCount = len(u.Applications)
		rows = append(rows, *u)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].UnitKey < rows[j].UnitKey })
	return rows
}

// handleUnitApplications implements GET /api/unit-applications — the landlord /
// property-manager operator console: every unit the SIGNED-IN landlord manages
// and the live applications against it, each with the applicant's name and full
// convergence disposition (the fine-grained gap/qualification detail the
// protected `read_landlord_lease_applications` model does not carry — see
// landlord_applications.go). Informational only — Approve/Decline is driven
// from the RLS-enforced read (landlord_applications.go).
// D1.5: this handler used to serve the entire
// platform's applicant roster with NO authentication at all (every landlord's
// units, every applicant's income/employment/reference signals, PII names) to
// any caller. It is now an AUTHENTICATED read, scoped to the caller's own units:
// the rich weaver-targets convergence rows are still assembled exactly as
// before (P5: never Core KV), but the response is filtered down to the unit
// keys the PROTECTED, RLS-enforced `read_landlord_lease_applications` model
// (queryLandlordApplications, the same source handleLandlordApplications reads)
// says this actor manages. Postgres RLS remains the single authorization source
// — this handler adds no authorization logic of its own, only a filter keyed
// off that authoritative set.
func (s *server) handleUnitApplications(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.logger.Error("unit-applications operator console requested but pgPool is nil (set LOFTSPACE_APP_PG_DSN + ensure Postgres and the lease-signing protected lens are up)")
		s.writeError(w, http.StatusBadGateway, "protected read model unavailable")
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	managed, err := queryLandlordApplications(ctx, s.pgPool, actor.Subject)
	if err != nil {
		s.logger.Error("read protected landlord lease applications", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected landlord lease-applications model")
		return
	}
	managedUnits := make(map[string]bool, len(managed))
	for _, row := range managed {
		if row.UnitKey != nil && *row.UnitKey != "" {
			managedUnits[*row.UnitKey] = true
		}
	}

	getter := func(bucket string) (kvGetter, []string, error) {
		keys, err := conn.KVListKeys(ctx, bucket)
		if err != nil {
			return nil, nil, err
		}
		get := func(key string) ([]byte, bool) {
			entry, err := conn.KVGet(ctx, bucket, key)
			if err != nil {
				return nil, false
			}
			return entry.Value, true
		}
		return get, keys, nil
	}

	appGet, appKeys, err := getter(bootstrap.WeaverTargetsBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bootstrap.WeaverTargetsBucket+": "+err.Error()+" (is lease-signing installed and the Refractor projecting?)")
		return
	}
	listGet, listKeys, err := getter(loftspacedomain.LoftspaceListingsBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+loftspacedomain.LoftspaceListingsBucket+": "+err.Error()+" (is loftspace-domain installed and the Refractor projecting?)")
		return
	}

	// Applicant names come from the PROTECTED applicantRosterRead Postgres
	// model — the only roster surface (the identity name is a sensitive aspect
	// the Secure Lens decrypts into that RLS-protected table alone). The read
	// runs as the app's OWN admin actor (the WildcardAnchor holder), not the
	// signed-in landlord: name resolution for display is the trusted-tool
	// server's job, while the response stays scoped to the caller's managed
	// units via the RLS-authoritative filter below. Names are display
	// decoration on this surface, so a missing admin actor degrades to bare
	// keys (logged) rather than failing the console; a QUERY failure against
	// a configured pool is a real infra fault and surfaces as 502.
	var identities []identityView
	if adminID, ok := s.adminActorID(); ok {
		identities, err = rosterIdentities(ctx, s.pgPool, adminID)
		if err != nil {
			s.logger.Error("read protected loftspace identities", "error", err)
			s.writeError(w, http.StatusBadGateway, "could not read the protected identities model")
			return
		}
	} else {
		s.logger.Warn("admin actor not loaded (BOOTSTRAP_JSON_PATH); applicant names degrade to bare keys")
	}

	apps := computeApplications(appKeys, appGet, "")
	listings := decodeListingProjections(listKeys, listGet)
	rows := groupByUnit(apps, identities, listings)
	scoped := filterUnitsToManaged(rows, managedUnits)
	s.writeJSON(w, http.StatusOK, map[string]any{"units": scoped, "count": len(scoped)})
}

// filterUnitsToManaged keeps only the rows whose UnitKey is in managed — the
// RLS-authoritative set of units the requesting actor manages
// (queryLandlordApplications). Always returns a non-nil slice (renders as []).
func filterUnitsToManaged(rows []unitApplicationsRow, managed map[string]bool) []unitApplicationsRow {
	scoped := make([]unitApplicationsRow, 0, len(rows))
	for _, row := range rows {
		if managed[row.UnitKey] {
			scoped = append(scoped, row)
		}
	}
	return scoped
}

// decodeListingProjections reads the `availableListings` read model into the
// flat projection shape (rent / address columns directly accessible), the seed
// for a unit that has no applications yet. A row that fails to decode or carries
// no unitKey (a tombstoned projection entry) is skipped.
func decodeListingProjections(keys []string, get kvGetter) []listingProjection {
	out := make([]listingProjection, 0, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p listingProjection
		if json.Unmarshal(raw, &p) != nil || p.UnitKey == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
