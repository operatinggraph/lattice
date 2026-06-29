package main

import (
	"encoding/json"
	"sort"
	"strings"
)

// applicationKeyPrefix is the OutputKeyPattern prefix of the lease-signing
// `leaseApplicationComplete` convergence lens (Contract #10 §10.2:
// "leaseApplicationComplete.{actorSuffix}"). It is read out of the shared
// weaver-targets read model — never Core KV (P5).
//
// This decode path serves the views that still read the convergence aggregate
// directly (the executed-lease document builder and the landlord by-unit view).
// The applicant-facing My Applications list (handleApplications) has moved to the
// PROTECTED, RLS-enforced Postgres read model (D1.3 Fire 3); D1.5 rolls the
// remaining views onto that pattern.
const applicationKeyPrefix = "leaseApplicationComplete."

// applicationRow is one projected `leaseApplicationComplete` row, the live state
// of a single lease application. The gap booleans drive the FE stepper; the
// inflight_ companion distinguishes "in progress" from "to do"; the declined_
// companion marks a standing business rejection (a failed check that no retry has
// superseded) so the FE shows "Declined" instead of a silent forever-"in review";
// the unit columns are the informational "what am I leasing" header. applicantApproved
// is true once the four APPLICANT gaps are all closed — but that means "qualified,
// pending the landlord decision," not "done." The landlord decision is the human gate
// the lease waits behind: landlordDecision carries the raw .decision value
// (approved|declined|""), landlordApproved/landlordDeclined are its booleans, and
// missing_decision marks a qualified application still awaiting that decision. The FE
// reads landlordApproved (+ unitStatus leased) for "complete," missing_decision for
// "awaiting landlord review," and declined (which now also covers a landlord decline)
// for the terminal rejection banner.
// maxretries_<g> is the lens's CONSTANT integer retry-budget cap baked onto every
// row (a count, not a flag — it is an int, not a bool: typing it bool drops every
// row on decode). unitRent is a pointer so an absent listing rent stays absent
// rather than 0.
type applicationRow struct {
	EntityKey          string   `json:"entityKey"`
	Applicant          string   `json:"applicant"`
	Violating          bool     `json:"violating"`
	ApplicantApproved  bool     `json:"applicantApproved"`
	LandlordDecision   string   `json:"landlordDecision"`
	LandlordApproved   bool     `json:"landlordApproved"`
	LandlordDeclined   bool     `json:"landlordDeclined"`
	DeclineReason      string   `json:"declineReason"`
	MissingDecision    bool     `json:"missing_decision"`
	MissingOnboarding  bool     `json:"missing_onboarding"`
	MissingBgcheck     bool     `json:"missing_bgcheck"`
	MissingPayment     bool     `json:"missing_payment"`
	MissingSignature   bool     `json:"missing_signature"`
	InflightBgcheck    bool     `json:"inflight_bgcheck"`
	InflightPayment    bool     `json:"inflight_payment"`
	DeclinedBgcheck    bool     `json:"declined_bgcheck"`
	DeclinedPayment    bool     `json:"declined_payment"`
	Declined           bool     `json:"declined"`
	MaxretriesBgcheck  int      `json:"maxretries_bgcheck"`
	MaxretriesPayment  int      `json:"maxretries_payment"`
	UnitKey            string   `json:"unitKey"`
	UnitAddress        string   `json:"unitAddress"`
	UnitCity           string   `json:"unitCity"`
	UnitRegion         string   `json:"unitRegion"`
	UnitRent           *float64 `json:"unitRent"`
	UnitCurrency       string   `json:"unitCurrency"`
	UnitBedrooms       *float64 `json:"unitBedrooms"`
	UnitBathrooms      *float64 `json:"unitBathrooms"`
	UnitLeaseTerm      *float64 `json:"unitLeaseTermMonths"`
	UnitAvailableFrom  string   `json:"unitAvailableFrom"`
	UnitStatus         string   `json:"unitStatus"`
	TermsMoveInDate    string   `json:"termsMoveInDate"`
	TermsLeaseTerm     *float64 `json:"termsLeaseTermMonths"`
	TermsRequestedRent *float64 `json:"termsRequestedRent"`
	SignedAt           string   `json:"signedAt"`
	FreshUntil         string   `json:"freshUntil"`
	// Applicant qualification profile — the DERIVED signals the landlord decides
	// on (SetApplicantProfile; the raw financials are never projected). All
	// pointers so an application with no .profile yet stays absent rather than
	// rendering a misleading false/0.
	ProfileSubmitted   bool  `json:"profileSubmitted"`
	IncomeToRentMet    *bool `json:"incomeToRentMet"`
	EmploymentVerified *bool `json:"employmentVerified"`
	ReferenceCount     *int  `json:"referenceCount"`
	HasCoApplicant     *bool `json:"hasCoApplicant"`
	HasGuarantor       *bool `json:"hasGuarantor"`
	// guarantorIncomeToRentMet — does the guarantor's own income cover 3× rent (the
	// standard reason a guarantor backs a thin-income applicant). Null until a
	// guarantor income is supplied against a unit with a known listing rent.
	GuarantorIncomeToRentMet *bool `json:"guarantorIncomeToRentMet"`
}

// computeApplications assembles application rows from the
// `leaseApplicationComplete` lens read model. It keeps only keys under the
// convergence prefix, decodes each row, and — when applicant is non-empty —
// keeps only that applicant's applications. A row that fails to decode or carries
// no entityKey (a tombstoned projection) is skipped. Rows sort by entityKey for a
// stable view.
//
// NOTE — this is the UNSCOPED weaver-targets decode (an `applicant` argument is a
// best-effort client-side filter, not an enforcement boundary). The applicant My
// Applications view no longer uses it; its remaining callers (the executed-lease
// builder, the landlord by-unit view) are operator/landlord aggregations that
// D1.5 will move onto the protected RLS pattern.
func computeApplications(keys []string, get kvGetter, applicant string) []applicationRow {
	rows := make([]applicationRow, 0)
	for _, k := range keys {
		if !strings.HasPrefix(k, applicationKeyPrefix) {
			continue
		}
		raw, ok := get(k)
		if !ok {
			continue
		}
		var row applicationRow
		if json.Unmarshal(raw, &row) != nil || row.EntityKey == "" {
			continue
		}
		if applicant != "" && row.Applicant != applicant {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].EntityKey < rows[j].EntityKey })
	return rows
}
