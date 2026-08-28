package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
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

// memberRow is one entry of the front desk's book-a-member picker. The
// covering set is deliberately NOT carried out to the client: it is the
// server's confinement input, not something the FE needs or should publish —
// a staffer learns which members they may book, never the topology that
// decided it.
type memberRow struct {
	BookerKey   string `json:"bookerKey"`
	LeaseAppKey string `json:"leaseAppKey"`
}

// computeCoveredMembers decodes every wellnessMembers row and keeps the ones
// this caller's workplace reaches, sorted for a stable picker order. A row
// that fails to decode or names no member is skipped (the tombstoned-entry
// guard computeBookings uses).
//
// Fails CLOSED throughout, the same construction cmd/cafe-app's
// staffCoveredLeases uses: an operator alone is unrestricted, a caller with no
// workplace covers nothing, and a member whose lease has not converged — or
// whose unit is unwired — is simply absent from the answer rather than
// defaulting to visible.
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
		if !hats.isOperator && !hats.covers(p.CoveringLocations) {
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

// handleMembers implements GET /api/members — the front desk's book-a-member
// picker, served from the wellnessMembers lens (P5) and scoped server-side to
// the members the caller's workplace covers.
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
			"the member directory is a staff surface for the place you work at")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := wellnessdomain.WellnessMembersBucket
	// A missing bucket is an ERROR, not an empty answer. This one is the
	// confinement source: an empty picker would read as "nobody is a member
	// here" rather than "this app cannot tell who you may book" — the same
	// distinction cmd/cafe-app draws for cafeLeaseWorkplaces.
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is wellness-domain 0.16.0 installed and the Refractor projecting?)")
		return
	}
	rows := computeCoveredMembers(keys, s.kvGetter(ctx, bucket), hats)
	s.writeJSON(w, http.StatusOK, map[string]any{"members": rows})
}
