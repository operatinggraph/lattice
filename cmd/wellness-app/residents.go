package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
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

// memberProjection is one row of the wellness-domain `wellnessMembers` lens —
// a member, the lease they hold, and the locations that cover them.
type memberProjection struct {
	LeaseAppKey       string   `json:"leaseAppKey"`
	BookerKey         string   `json:"bookerKey"`
	LandlordDecision  string   `json:"landlordDecision"`
	CoveringLocations []string `json:"coveringLocations"`
}

// declinedDecision is the one landlordDecision value that removes somebody from
// the directory. Compared as an allow-nothing-else test rather than a
// "keep only approved" one on purpose: the column is three-state, and an
// application still awaiting a landlord belongs to somebody living in the
// building whom the front desk books in. Only a REFUSAL is disqualifying.
const declinedDecision = "declined"

// bookerProjection is one row of the wellness-domain `wellnessBookers` lens —
// a live booking, the person who made it, and the locations covering the class
// they booked. It is how somebody holding no lease reaches the desk at all.
type bookerProjection struct {
	BookingKey        string   `json:"bookingKey"`
	BookerKey         string   `json:"bookerKey"`
	Status            string   `json:"status"`
	CoveringLocations []string `json:"coveringLocations"`
}

// memberRow is one entry of the front desk's book-a-member picker. A blank
// LeaseAppKey is a GUEST — somebody the desk reaches through the class they
// booked rather than through a lease. The covering set is deliberately NOT
// carried out to the client: it is the server's confinement input, not
// something the FE needs or should publish — a staffer learns which members
// they may book, never the topology that decided it.
type memberRow struct {
	BookerKey   string `json:"bookerKey"`
	LeaseAppKey string `json:"leaseAppKey"`
}

// hatsReachCoverage is the confinement test every member-directory row passes,
// whichever lens it came from. Fails CLOSED: an operator alone is unrestricted,
// a workplace covers nothing without isFrontDesk alongside it (covers is a
// structural fact, never sufficient on its own — readauth.go), and an empty
// covering set reaches nobody.
func hatsReachCoverage(hats subjectHats, coveringLocations []string) bool {
	return hats.isOperator || (hats.isFrontDesk() && hats.covers(coveringLocations))
}

// computeCoveredMembers decodes every wellnessMembers row and keeps the ones
// this caller's front desk reaches, sorted for a stable picker order. A row
// that fails to decode or names no member is skipped (the tombstoned-entry
// guard computeBookings uses).
//
// Fails CLOSED throughout, the same construction cmd/cafe-app's
// staffCoveredLeases uses: an operator alone is unrestricted, a workplace
// covers nothing without isFrontDesk alongside it (covers is a structural
// fact, never sufficient on its own — readauth.go), and a member whose lease
// has not converged — or whose unit is unwired — is simply absent from the
// answer rather than defaulting to visible.
func computeCoveredMembers(keys []string, get kvGetter, hats subjectHats) []memberRow {
	rows := make([]memberRow, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p memberProjection
		if json.Unmarshal(raw, &p) != nil || p.BookerKey == "" || p.LeaseAppKey == "" {
			continue
		}
		// A refused applicant keeps a live lease and a live applicationFor link
		// (DecideLeaseApplication tombstones neither), so dropping them is this
		// reader's job. The operator exemption below is from CONFINEMENT, not
		// from this: root sees every building, not people it was never true to
		// call members.
		if strings.EqualFold(strings.TrimSpace(p.LandlordDecision), declinedDecision) {
			continue
		}
		if !hatsReachCoverage(hats, p.CoveringLocations) {
			continue
		}
		rows = append(rows, memberRow{BookerKey: p.BookerKey, LeaseAppKey: p.LeaseAppKey})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].BookerKey != rows[j].BookerKey {
			return rows[i].BookerKey < rows[j].BookerKey
		}
		return rows[i].LeaseAppKey < rows[j].LeaseAppKey
	})
	return rows
}

// computeCoveredBookers decodes every wellnessBookers row and keeps the ones
// this caller's front desk reaches, one row per PERSON — the lens is one row
// per booking, so a guest with three classes at the same building is one entry
// here. Every row carries a blank LeaseAppKey: coverage came from the class,
// not from a tenancy, and there is no lease to hand CreateBooking for a
// resident rate.
//
// Same fail-closed construction as computeCoveredMembers: a row that fails to
// decode or names no booker is skipped, and a booker whose class has no wired
// location — or whose session was called off, leaving the coverage set empty —
// is absent from the answer rather than defaulting to visible.
func computeCoveredBookers(keys []string, get kvGetter, hats subjectHats) []memberRow {
	rows := make([]memberRow, 0)
	seen := make(map[string]bool)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p bookerProjection
		if json.Unmarshal(raw, &p) != nil || p.BookerKey == "" {
			continue
		}
		if !hatsReachCoverage(hats, p.CoveringLocations) {
			continue
		}
		if seen[p.BookerKey] {
			continue
		}
		seen[p.BookerKey] = true
		rows = append(rows, memberRow{BookerKey: p.BookerKey})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].BookerKey < rows[j].BookerKey })
	return rows
}

// coveredMembers is the one answer every front-desk money surface confines
// through: the members this caller reaches by LEASE, then the bookers they
// reach by CLASS and hold no lease row for. Both are confinement sources, so a
// missing bucket on either of them is an ERROR rather than an empty answer —
// an empty picker would read as "nobody is a member here" instead of "this
// app cannot tell who you may book". The two are unioned into ONE list,
// deterministically sorted: lease rows first (by LeaseAppKey), then guest rows
// (by BookerKey).
//
// The lease rows come first and a booker already named by one is dropped: a
// member's row carries the leaseapp key their resident rate is derived from,
// which the guest row cannot supply.
func (s *server) coveredMembers(ctx context.Context, conn *substrate.Conn, hats subjectHats) ([]memberRow, error) {
	membersBucket := wellnessdomain.WellnessMembersBucket
	memberKeys, err := conn.KVListKeys(ctx, membersBucket)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w (is wellness-domain installed and the Refractor projecting?)", membersBucket, err)
	}
	bookersBucket := wellnessdomain.WellnessBookersBucket
	bookerKeys, err := conn.KVListKeys(ctx, bookersBucket)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w (is wellness-domain installed and the Refractor projecting?)", bookersBucket, err)
	}

	rows := computeCoveredMembers(memberKeys, s.kvGetter(ctx, membersBucket), hats)
	leased := make(map[string]bool, len(rows))
	for _, row := range rows {
		leased[row.BookerKey] = true
	}
	for _, row := range computeCoveredBookers(bookerKeys, s.kvGetter(ctx, bookersBucket), hats) {
		if leased[row.BookerKey] {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// handleMembers implements GET /api/members — the front desk's book-a-member
// picker, served from the wellnessMembers and wellnessBookers lenses (P5) and
// scoped server-side to the people the caller's workplace covers. A row with a
// blank leaseAppKey is a guest, reached through the class they booked rather
// than through a lease.
//
// It is a STAFF surface: a member is refused outright rather than served their
// own row, because a directory of who else lives here is not a member's to
// read — that is the boundary the old unscoped /api/residents lacked, and the
// reason deleting it was correct. An instructor is refused for the same
// reason; leading a class confers no directory.
//
// The picker is an affordance, not the authority. `CreateBooking` confines a
// front-desk caller by the SESSION's location, never by who the booker is
// (packages/wellness-domain/permissions.go), so this narrows what a staffer is
// OFFERED and the Starlark guard still decides what they may write.
func (s *server) handleMembers(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	hats, err := s.resolveSubjectHats(r)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	if !hats.isFrontDesk() && !hats.isOperator {
		s.writeError(w, http.StatusForbidden,
			"the member directory is a front-desk surface for the place you work at")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	rows, err := s.coveredMembers(ctx, conn, hats)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"members": rows})
}
