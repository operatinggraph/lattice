//go:build ignore

// seed-classic-demo.go — dev-seed for `make seed-classic-demo`.
//
// verticals.md "Classic vertical demo data has no seed path": a fresh dev
// stack's Core KV holds zero leaseapp/listing/appointment/tab vertices —
// seed-edge-demo and seed-showcase both mint Facet catalog scaffolding only,
// nothing that exercises the classic (non-Facet) LoftSpace/Clinic/Café
// flows. This seeds one of each: a LoftSpace unit + available listing + a
// consumer's lease application, a Clinic patient + provider + appointment
// (linked to the lease so PO discovery can walk resident->tenant->patient),
// a Café tab opened against that same lease, two menu-catalog items so the
// tab's self-order picker has something to show, and a Wellness studio with
// one bookable session.
//
// Requires `make install-showcase-domains` (loftspace/clinic/cafe/wellness
// domains) already applied to the target stack — the domain ops below FATAL
// on an uninstalled package.
//
// Every op below is submitted directly over NATS as the bootstrap admin
// actor (already `operator` via the primordial seed) — mirroring
// seed-edge-demo.go / verify-real-actor-write-auth.go's seedListing helper,
// not the Gateway.
//
// NOT idempotent: mints fresh vertices on every run (no dedup key), matching
// seed-edge-demo.go's own dev-loop convention — EXCEPT the entities that land
// in a roster or browsable inventory shared across every run, which use
// fixed, checked-in handles (patientID / providerID / unitID / studioID
// below, mirroring seed-showcase.go's idempotency seam): an unpinned id let a
// rerun mint a second "Dr. Classic Demo" / "Classic Demo Patient" /
// "12 Classic Demo Ave" / "Classic Demo Studio" that the booking picker,
// patient roster, LoftSpace inventory, or /api/studios then rendered as an
// indistinguishable duplicate. The appointment and (below) the session stay
// day-derived rather than fixed, so a same-day rerun still converges without
// permanently pinning a single calendar slot.
//
// Run via: make seed-classic-demo (== go run ./scripts/seed-classic-demo.go)
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/scripts/pkgverify"
)

// Fixed, checked-in handles (Contract #1 — valid 20-char canonical NanoIDs,
// generated once via substrate.NewNanoID and pinned here) — the idempotency
// seam entities shared across every run must converge on rather than
// duplicate, mirroring seed-showcase.go's convention. unitID/studioID join
// patientID/providerID for the same reason: an unpinned CreateLocation /
// CreateStudio mints a fresh vertex every rerun, and LoftSpace's browsable
// inventory + /api/studios both rendered the resulting pile of
// indistinguishable "12 Classic Demo Ave" / "Classic Demo Studio" duplicates.
// latteID/croissantID join them for the same reason: an unpinned
// CreateMenuItem minted a fresh pair every rerun, and the staff Manage Menu
// grid + the self-order picker both rendered the resulting pile of
// indistinguishable "Latte" / "Croissant" duplicates.
const (
	patientID   = "rQ7RnR5XWyZP2BSropUD"
	providerID  = "zJjptLYizx4vDU6KRt9i"
	unitID      = "7mRGqbqxmZqPV9HY12T2"
	studioID    = "neEA76zkT84xv8tc6CNX"
	latteID     = "qvVewSfyQZcWCnFAC3sY"
	croissantID = "8rSutmvxePuueAZSCP8J"

	// rileyPatientID / riversideBuildingID are seed-showcase.go's own pinned
	// ids (transcribed, not imported — each seed script is a standalone `go
	// run` file). rileyPatientID is needed only as the OTHER entry in
	// reapNonCanonicalPatients'/reapNonCanonicalSites' keep-allowlists: the
	// live clinic roster/site directory (verticals.md "the patient-facing
	// site picker offers a verify artifact as a clinic") has accrued
	// verify-fire litter whose display names don't share one naming
	// convention ("Grid Snap Test Patient", "Retention Proof" — neither
	// contains "Verify"/"Discovery", the marker isVerifyLitterName checks),
	// so reaping by name would miss rows a naming sweep can't predict.
	// Reaping by allowlist instead only requires knowing every checked-in
	// script's canonical id, which is already this file's own convention.
	// riversideBuildingID doubles as that allowlist entry and as the parent
	// this seed's own unit wires containedIn into (below), so leases on it
	// are covered by any front-desk staffer whose worksAt anchors there.
	rileyPatientID      = "w5sDPrw4eraPfUHk96wo"
	riversideBuildingID = "A9jnKK2bGwZNrfHHkLme"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	natsURL := pkgverify.EnvOrDefault("NATS_URL", "nats://localhost:4222")
	bootstrapPath := pkgverify.EnvOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")

	must(bootstrap.Load(bootstrapPath), "load bootstrap JSON")

	conn, err := output.Connect(ctx, natsURL)
	must(err, "connect to NATS")
	defer conn.Close()

	adminKey := bootstrap.BootstrapIdentityKey
	consumerRoleKey := "vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")

	// --- LoftSpace: unit + listing + consumer + lease application -----------

	unitKey := "vtx.unit." + unitID
	if !alive(ctx, conn, unitKey) {
		submitOp(ctx, conn, adminKey, "CreateLocation", "unit",
			map[string]any{"locationType": "unit", "locationId": unitID,
				"presentation": map[string]any{"name": "Unit 1", "icon": "door"}}, nil)
	}
	fmt.Printf("==> unit:            %s\n", unitKey)

	// Best-effort: wires the unit into seed-showcase.go's Riverside Building
	// if that world has already been seeded, so a front-desk staffer whose
	// worksAt anchor sits at the building (cafe-domain's coveringLocations
	// walks containedIn) covers this unit's leases too. A standalone run of
	// this seed (no seed-showcase) leaves the unit unwired, same as today.
	riversideBuildingKey := "vtx.building." + riversideBuildingID
	if alive(ctx, conn, riversideBuildingKey) && !alive(ctx, conn, linkKey(unitKey, "containedIn", riversideBuildingKey)) {
		submitOp(ctx, conn, adminKey, "WireContainedIn", "unit",
			map[string]any{"child": unitKey, "parent": riversideBuildingKey},
			&processor.ContextHint{Reads: []string{unitKey, riversideBuildingKey}})
		fmt.Println("==> wired:           unit containedIn " + riversideBuildingKey)
	}

	if !alive(ctx, conn, unitKey+".address") {
		submitOp(ctx, conn, adminKey, "SetUnitAddress", "loftspaceListing",
			map[string]any{"unit": unitKey, "line1": "12 Classic Demo Ave", "city": "Springfield", "region": "OR", "postal": "97477"},
			&processor.ContextHint{Reads: []string{unitKey}})
	}
	fmt.Println("==> wired:           unit address")

	if !alive(ctx, conn, unitKey+".listing") {
		submitOp(ctx, conn, adminKey, "SetListing", "loftspaceListing",
			map[string]any{"unit": unitKey, "rentAmount": 2200, "rentCurrency": "USD", "bedrooms": 1,
				"availableFrom": "2026-08-01T00:00:00Z", "leaseTermMonths": 12, "status": "available"},
			&processor.ContextHint{Reads: []string{unitKey}})
	}
	fmt.Printf("==> listing:         %s (available)\n", unitKey)

	// DecideLeaseApplication's self-scoped grant is authorized off the
	// landlord's `manages` link (require_manages) — with no landlord persona
	// this world's application can never be decided outside Loupe/operator.
	landlordKey := ensureLandlord(ctx, conn, adminKey, unitKey, consumerRoleKey)
	fmt.Printf("==> landlord:        %s manages %s\n", landlordKey, unitKey)
	reapExcessCoManagers(ctx, conn, adminKey, unitKey, landlordKey)
	reapExcessCoManagersLive(ctx, conn, adminKey)
	reapDuplicateListings(ctx, conn, adminKey, unitKey)
	backfillBareListings(ctx, conn, adminKey)
	backfillUnownedListings(ctx, conn, adminKey, consumerRoleKey)

	salt, err := substrate.NewNanoID()
	must(err, "generate consumer email salt")
	claimSum := mustSHA256Hex("classic-demo-consumer-" + salt)
	consumerReply := submitOp(ctx, conn, adminKey, "CreateUnclaimedIdentity", "identity",
		map[string]any{
			"name":         "Classic Demo Resident " + salt[:8],
			"email":        "resident-" + salt[:8] + "@dev.lattice.local",
			"claimKeyHash": claimSum,
		}, nil)
	consumerKey := consumerReply.PrimaryKey
	fmt.Printf("==> resident:        %s\n", consumerKey)

	submitOp(ctx, conn, adminKey, "AssignRole", "",
		map[string]any{"actorKey": consumerKey, "roleKey": consumerRoleKey},
		&processor.ContextHint{Reads: []string{consumerKey, consumerRoleKey}})
	fmt.Printf("==> assigned:        %s holds consumer (%s)\n", consumerKey, consumerRoleKey)

	leaseReply := submitOp(ctx, conn, adminKey, "CreateLeaseApplication", "leaseapp",
		map[string]any{"applicant": consumerKey, "unit": unitKey},
		&processor.ContextHint{Reads: []string{consumerKey, unitKey}})
	leaseAppKey := leaseReply.PrimaryKey
	fmt.Printf("==> lease app:       %s\n", leaseAppKey)

	// --- Clinic: patient + provider + appointment ----------------------------

	patientKey := "vtx.patient." + patientID
	if !alive(ctx, conn, patientKey) {
		submitOp(ctx, conn, adminKey, "CreatePatient", "patient",
			map[string]any{"fullName": "Classic Demo Patient", "patientId": patientID}, nil)
	}
	fmt.Printf("==> patient:         %s\n", patientKey)
	reapNonCanonicalPatients(ctx, conn, adminKey, map[string]bool{
		patientKey:                      true,
		"vtx.patient." + rileyPatientID: true,
	})
	reapNonCanonicalSites(ctx, conn, adminKey, map[string]bool{
		"vtx.building." + riversideBuildingID: true,
	})

	providerKey := "vtx.provider." + providerID
	if !alive(ctx, conn, providerKey) {
		submitOp(ctx, conn, adminKey, "CreateProvider", "provider",
			map[string]any{"fullName": "Dr. Classic Demo", "specialty": "Family Medicine", "providerId": providerID}, nil)
	}
	fmt.Printf("==> provider:        %s\n", providerKey)
	reapDuplicateProviders(ctx, conn, adminKey, providerKey)

	// Unconditioned SetProviderHours upsert (no OCC/guard aspect, config not a
	// write-path claim key) — one window per UTC weekday, 08:00-18:00, wide
	// enough to always cover the 14:00 appointment below regardless of which
	// weekday it lands on. Without it the provider carries no .hours aspect;
	// CreateAppointment still accepts the booking (enforce_hours treats an
	// absent aspect as unconstrained), but the FE slot picker
	// (computeOpenSlots, cmd/clinic-app/web/app.js) only ever proposes slots
	// from an explicit window, so it would offer none for this provider.
	hoursWindows := make([]any, 0, 7)
	for day := 0; day < 7; day++ {
		hoursWindows = append(hoursWindows, map[string]any{"day": day, "openSec": 8 * 3600, "closeSec": 18 * 3600})
	}
	submitOp(ctx, conn, adminKey, "SetProviderHours", "provider",
		map[string]any{"providerKey": providerKey, "windows": hoursWindows},
		&processor.ContextHint{Reads: []string{providerKey}})

	// Fixed hour on the day 24h out (on the clinic's mandatory 15-minute grid,
	// :00/:15/:30/:45) rather than time.Now() truncated — deterministic per
	// day, mirroring seed-showcase.go's futureDayAt idiom, so a same-day
	// rerun derives the SAME appointmentId and converges via the alive()
	// guard instead of double-booking the now-fixed provider (SlotConflict).
	apptDay := time.Now().UTC().Add(24 * time.Hour).Truncate(24 * time.Hour)
	startsAt := apptDay.Add(14 * time.Hour)
	endsAt := startsAt.Add(30 * time.Minute)
	apptID := substrate.DeriveNanoID("classic-demo-appointment", apptDay.Format("2006-01-02"))
	apptKey := "vtx.appointment." + apptID
	if !alive(ctx, conn, apptKey) {
		// No `site` param here, deliberately: unlike seed-showcase.go's Riley
		// world, this seed's ONLY location vertex is unitKey (CreateLocation
		// {locationType: "unit"} above) — there is no vtx.building in this
		// world at all, so there is nothing to AssignProviderSite this
		// provider to and no site key CreateAppointment could validate
		// against. Minting a building purely to satisfy the optional site
		// param would be scope this seed's world does not otherwise need.
		submitOp(ctx, conn, adminKey, "CreateAppointment", "appointment",
			map[string]any{
				"patient":       patientKey,
				"provider":      providerKey,
				"appointmentId": apptID,
				"startsAt":      startsAt.Format(time.RFC3339),
				"endsAt":        endsAt.Format(time.RFC3339),
				"reason":        "Annual checkup",
				"leaseAppKey":   leaseAppKey,
			},
			&processor.ContextHint{
				Reads: []string{patientKey, providerKey},
				OptionalReads: append(
					slotClaimKeys(providerKey, startsAt, endsAt),
					slotClaimKeys(patientKey, startsAt, endsAt)...),
			})
	}
	fmt.Printf("==> appointment:     %s (%s)\n", apptKey, startsAt.Format(time.RFC3339))
	backfillClinicForwardSchedule(ctx, conn, adminKey, patientKey, providerKey)
	reapClinicVerifyLitterAppointments(ctx, conn, adminKey)

	// --- Café: tab opened against the same lease ------------------------------

	tabReply := submitOp(ctx, conn, adminKey, "OpenTab", "tab",
		map[string]any{"leaseAppKey": leaseAppKey},
		&processor.ContextHint{
			Reads:         []string{leaseAppKey},
			OptionalReads: []string{leaseAppKey + ".cafeOpenTab"},
		})
	fmt.Printf("==> tab:             %s (open)\n", tabReply.PrimaryKey)
	reapGhostLeases(ctx, conn, adminKey)
	reapVerifyTenantLease(ctx, conn)

	// Both items are served at the demo unit, which is where the resident
	// lives — the residence chain a Facet browse walk descends binds the unit
	// itself at depth 0, so the catalog is reachable from the first hop.
	menuLocationHint := &processor.ContextHint{Reads: []string{unitKey}}
	latteKey := "vtx.menuitem." + latteID
	if !alive(ctx, conn, latteKey) {
		submitOp(ctx, conn, adminKey, "CreateMenuItem", "menuitem",
			map[string]any{"name": "Latte", "priceCents": 450, "locationKey": unitKey, "menuItemId": latteID}, menuLocationHint)
	}
	fmt.Printf("==> menu item:       %s (Latte, $4.50)\n", latteKey)
	croissantKey := "vtx.menuitem." + croissantID
	if !alive(ctx, conn, croissantKey) {
		submitOp(ctx, conn, adminKey, "CreateMenuItem", "menuitem",
			map[string]any{"name": "Croissant", "priceCents": 350, "locationKey": unitKey, "menuItemId": croissantID}, menuLocationHint)
	}
	fmt.Printf("==> menu item:       %s (Croissant, $3.50)\n", croissantKey)
	reapDuplicateMenuItems(ctx, conn, adminKey, unitKey, map[string]bool{latteKey: true, croissantKey: true})
	backfillMenuItemLocations(ctx, conn, adminKey, unitKey)

	// --- Wellness: studio + bookable session ---------------------------------

	studioKey := "vtx.studio." + studioID
	if !alive(ctx, conn, studioKey) {
		submitOp(ctx, conn, adminKey, "CreateStudio", "studio",
			map[string]any{"name": "Classic Demo Studio", "studioId": studioID, "location": unitKey},
			&processor.ContextHint{Reads: []string{unitKey}})
	}
	fmt.Printf("==> studio:          %s\n", studioKey)
	reapDuplicateStudios(ctx, conn, adminKey, studioKey)

	// Same 15-minute grid the clinic's appointment uses; the wellness DDL
	// rejects an unaligned span with SlotGridViolation. Day-derived like the
	// appointment's apptID above, so a same-day rerun converges on the same
	// session instead of double-booking the now-fixed studio.
	sessionStart := time.Now().UTC().Add(24 * time.Hour).Truncate(15 * time.Minute)
	sessionEnd := sessionStart.Add(time.Hour)
	sessionID := substrate.DeriveNanoID("classic-demo-session", sessionStart.Format("2006-01-02"))
	sessionKey := "vtx.session." + sessionID

	if !alive(ctx, conn, sessionKey) {
		submitOp(ctx, conn, adminKey, "CreateSession", "session",
			map[string]any{
				"studio":     studioKey,
				"sessionId":  sessionID,
				"name":       "Vinyasa Flow",
				"startsAt":   sessionStart.Format(time.RFC3339),
				"endsAt":     sessionEnd.Format(time.RFC3339),
				"capacity":   12,
				"priceCents": 1500,
			},
			&processor.ContextHint{
				Reads: []string{studioKey},
				// The studio's per-cell slot claims: absent until something
				// books the cell, so optional (Contract #2 §2.5 read-before-create).
				OptionalReads: slotClaimKeys(studioKey, sessionStart, sessionEnd),
			})
	}
	fmt.Printf("==> session:         %s (%s, capacity 12)\n", sessionKey, sessionStart.Format(time.RFC3339))
	reapVerifyLitter(ctx, conn, adminKey)

	// --- Café front desk: a live class + visit badge for an already-tenanted resident ---

	backfillCafeFrontDeskBadges(ctx, conn, adminKey, sessionKey, providerKey)

	fmt.Println()
	fmt.Println("==> classic vertical demo data seeded.")
	fmt.Printf("    resident:    %s\n", consumerKey)
	fmt.Printf("    landlord:    %s\n", landlordKey)
	fmt.Printf("    lease app:   %s\n", leaseAppKey)
	fmt.Printf("    listing:     %s\n", unitKey)
	fmt.Printf("    appointment: %s\n", apptKey)
	fmt.Printf("    tab:         %s\n", tabReply.PrimaryKey)
	fmt.Printf("    studio:      %s\n", studioKey)
	fmt.Printf("    session:     %s\n", sessionKey)
}

// linkKey builds the deterministic 6-segment link key (Contract #1 §1) for a
// source--relation-->target triple, mirroring seed-showcase.go's helper of
// the same name (each seed script is a standalone `go run` file, so the
// helper is not shared).
func linkKey(source, relation, target string) string {
	return "lnk." + strings.TrimPrefix(source, "vtx.") + "." + relation + "." + strings.TrimPrefix(target, "vtx.")
}

// slotClaimKeys enumerates the 15-minute cells [start, end) covers into the
// studio's slot-claim aspect keys, mirroring wellness-domain's slot_cells +
// slot_cellcode Starlark helpers (strip '-'/':' and lowercase) so this
// dispatcher can declare them, script-read-posture-design.md §13.
func slotClaimKeys(hub string, start, end time.Time) []string {
	var keys []string
	for cur := start; cur.Before(end); cur = cur.Add(15 * time.Minute) {
		code := strings.ToLower(strings.NewReplacer("-", "", ":", "").Replace(cur.UTC().Format(time.RFC3339)))
		keys = append(keys, hub+".slot"+code)
	}
	return keys
}

// reapDuplicateMenuItems tombstones every live "Latte"/"Croissant" menu item
// served at unitKey other than the checked-in canonical pair (keep) — c79f9b5f
// pinned CreateMenuItem's ids going forward so a rerun converges instead of
// duplicating, but never reaped the copies an unpinned rerun had already
// minted before that fix landed, so the staff Manage Menu grid and the
// self-order picker still render every one of them (verticals.md). Filtered
// by the servedAt link to THIS seed's own unitKey, not by name alone — a
// different demo (seed-showcase.go) mints its own, differently-ID'd
// "Latte"/"Croissant" pair at its own location, and must not be touched here.
// Direct Core KV reads + the same admin-actor op-submission every other
// mutation in this file already uses (P2: state still changes only via
// RetireMenuItem, never a raw KV write) — sanctioned here only because this
// is a dev/ops loader, not a P5 vertical-app read path (mirrors alive/
// findApplicantLeaseApp's identical rationale, seed-showcase.go).
func reapDuplicateMenuItems(ctx context.Context, conn *substrate.Conn, adminKey, unitKey string, keep map[string]bool) {
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.menuitem.")
	must(err, "list vtx.menuitem. keys")
	for _, key := range keys {
		if keep[key] || !alive(ctx, conn, key) || !alive(ctx, conn, linkKey(key, "servedAt", unitKey)) {
			continue
		}
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key+".price")
		if err != nil {
			continue
		}
		var aspect struct {
			IsDeleted bool `json:"isDeleted"`
			Data      struct {
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := json.Unmarshal(entry.Value, &aspect); err != nil || aspect.IsDeleted {
			continue
		}
		if aspect.Data.Name != "Latte" && aspect.Data.Name != "Croissant" {
			continue
		}
		submitOp(ctx, conn, adminKey, "RetireMenuItem", "menuitem",
			map[string]any{"menuItemKey": key},
			&processor.ContextHint{Reads: []string{key}})
		fmt.Printf("==> reaped duplicate menu item: %s (%s)\n", key, aspect.Data.Name)
	}
}

// backfillMenuItemLocations relocates every live menu item whose servedAt
// link is dead or absent onto the live canonical unitKey, using the new
// SetMenuItemLocation op (verticals.md "a menu item outlives the place that
// served it, with no flag and no way back": TombstoneLocation doesn't
// cascade onto a menu item's servedAt link, so retiring a unit strands every
// item still pointed at it — the same gap the studio-side ReassignSession
// closes, wellness-domain). Idempotent: an item already served at a live
// location, unitKey or otherwise, is untouched.
func backfillMenuItemLocations(ctx context.Context, conn *substrate.Conn, adminKey, unitKey string) {
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.menuitem.")
	must(err, "list vtx.menuitem. keys")
	for _, key := range keys {
		if strings.Count(key, ".") != 2 || !alive(ctx, conn, key) || servedAtIsLive(ctx, conn, key) {
			continue
		}
		submitOp(ctx, conn, adminKey, "SetMenuItemLocation", "menuitem",
			map[string]any{"menuItemKey": key, "newLocation": unitKey},
			&processor.ContextHint{
				Reads:         []string{key, unitKey},
				OptionalReads: []string{linkKey(key, "servedAt", unitKey)},
			})
		fmt.Printf("==> relocated menu item: %s -> %s\n", key, unitKey)
	}
}

// servedAtIsLive reports whether a menu item carries a live servedAt link to
// a still-alive location.
func servedAtIsLive(ctx context.Context, conn *substrate.Conn, menuItemKey string) bool {
	prefix := "lnk." + strings.TrimPrefix(menuItemKey, "vtx.") + ".servedAt."
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, prefix)
	must(err, "list "+prefix+" keys")
	for _, k := range keys {
		if alive(ctx, conn, k) && alive(ctx, conn, "vtx."+strings.TrimPrefix(k, prefix)) {
			return true
		}
	}
	return false
}

// reapDuplicateProviders tombstones every live "Dr. Classic Demo" provider
// other than the checked-in canonical one (keep) — adbf2571 pinned the
// seed's provider id going forward but never reaped the pre-pin duplicates,
// so the patient booking picker still offers all of them, none carrying
// hours or a practicesAt site (verticals.md). Name-filtered only (no
// location join at CreateProvider time to filter on, unlike menu items) —
// safe because only this script ever mints a provider with this exact
// fullName (seed-showcase.go's own provider uses a different name/id).
func reapDuplicateProviders(ctx context.Context, conn *substrate.Conn, adminKey, keep string) {
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.provider.")
	must(err, "list vtx.provider. keys")
	for _, key := range keys {
		if key == keep || !alive(ctx, conn, key) {
			continue
		}
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key+".profile")
		if err != nil {
			continue
		}
		var aspect struct {
			IsDeleted bool `json:"isDeleted"`
			Data      struct {
				FullName string `json:"fullName"`
			} `json:"data"`
		}
		if err := json.Unmarshal(entry.Value, &aspect); err != nil || aspect.IsDeleted {
			continue
		}
		if aspect.Data.FullName != "Dr. Classic Demo" {
			continue
		}
		submitOp(ctx, conn, adminKey, "TombstoneProvider", "provider",
			map[string]any{"providerKey": key},
			&processor.ContextHint{Reads: []string{key}})
		fmt.Printf("==> reaped duplicate provider: %s (%s)\n", key, aspect.Data.FullName)
	}
}

// backfillClinicForwardSchedule mints a handful of additional future
// appointments for the same patient/provider (verticals.md "the clinic has
// no forward schedule, so the front desk's day view is permanently empty":
// every appointment any script has ever seeded eventually falls into the
// past and MarkPastDueNoShow ages it into noShow, and /api/staff/appointments
// applies no server-side time filter — cmd/clinic-app/web/app.js's
// day/week calendar view renders nothing once the last forward-dated
// appointment ages out). Four separate days, well clear of the single 24h-out
// appointment above, so none collide with it or each other. Day-derived
// deterministic ids, like that appointment, so a rerun converges on the same
// four instead of piling up a fresh set each time.
func backfillClinicForwardSchedule(ctx context.Context, conn *substrate.Conn, adminKey, patientKey, providerKey string) {
	for _, offsetDays := range []int{3, 5, 8, 12} {
		day := time.Now().UTC().Add(time.Duration(offsetDays) * 24 * time.Hour).Truncate(24 * time.Hour)
		startsAt := day.Add(10 * time.Hour)
		endsAt := startsAt.Add(30 * time.Minute)
		apptID := substrate.DeriveNanoID("classic-demo-appointment-forward", day.Format("2006-01-02"))
		apptKey := "vtx.appointment." + apptID
		if alive(ctx, conn, apptKey) {
			continue
		}
		submitOp(ctx, conn, adminKey, "CreateAppointment", "appointment",
			map[string]any{
				"patient":       patientKey,
				"provider":      providerKey,
				"appointmentId": apptID,
				"startsAt":      startsAt.Format(time.RFC3339),
				"endsAt":        endsAt.Format(time.RFC3339),
				"reason":        "Follow-up visit",
			},
			&processor.ContextHint{
				Reads: []string{patientKey, providerKey},
				OptionalReads: append(
					slotClaimKeys(providerKey, startsAt, endsAt),
					slotClaimKeys(patientKey, startsAt, endsAt)...),
			})
		fmt.Printf("==> forward appointment: %s (%s)\n", apptKey, startsAt.Format(time.RFC3339))
	}
}

// clinicVerifyLitterReasons is the exact set of .schedule.reason values a
// live-stack sweep found on ad hoc verify-*.go / PO-discovery appointments
// (verticals.md "5 also carry agent-verify reasons") — an allowlist rather
// than a substring marker like isVerifyLitterName's, because these five don't
// share one naming convention ("PO site-projection probe" contains neither
// "Verify" nor "Discovery").
var clinicVerifyLitterReasons = map[string]bool{
	"staff-worlds F3 workplace-anchor vector": true,
	"inc2a live verify":                       true,
	"PO discovery self-book":                  true,
	"PO site-projection probe":                true,
}

// reapClinicVerifyLitterAppointments tombstones every live appointment whose
// visit reason matches clinicVerifyLitterReasons. TombstoneAppointment
// requires the appointment's own patient/provider (to release their held
// slot-claim cells), read off its forPatient/withProvider links — mirroring
// findSessionStudio's link-scan shape below.
func reapClinicVerifyLitterAppointments(ctx context.Context, conn *substrate.Conn, adminKey string) {
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.appointment.")
	must(err, "list vtx.appointment. keys")
	for _, key := range keys {
		if strings.Count(key, ".") != 2 || !alive(ctx, conn, key) {
			continue
		}
		reason, ok := readAspectReason(ctx, conn, key+".schedule")
		if !ok || !clinicVerifyLitterReasons[reason] {
			continue
		}
		patientKey, providerKey, found := findAppointmentPatientProvider(ctx, conn, key)
		if !found {
			continue
		}
		submitOp(ctx, conn, adminKey, "TombstoneAppointment", "appointment",
			map[string]any{"appointmentKey": key, "patient": patientKey, "provider": providerKey},
			&processor.ContextHint{Reads: []string{key, patientKey, providerKey}})
		fmt.Printf("==> reaped verify-litter appointment: %s (%s)\n", key, reason)
	}
}

// readAspectReason reads a {isDeleted, data:{reason}} aspect and returns its
// reason when the aspect is alive — the appointmentSchedule shape's sibling
// to readAspectName's {data:{name}} shape below.
func readAspectReason(ctx context.Context, conn *substrate.Conn, aspectKey string) (string, bool) {
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, aspectKey)
	if err != nil {
		return "", false
	}
	var aspect struct {
		IsDeleted bool `json:"isDeleted"`
		Data      struct {
			Reason string `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &aspect); err != nil || aspect.IsDeleted {
		return "", false
	}
	return aspect.Data.Reason, true
}

// findAppointmentPatientProvider resolves an appointment's live forPatient /
// withProvider link targets, mirroring findSessionStudio's atStudio scan
// below (Contract #1 §1.1: the appointment is the later-arriving source).
func findAppointmentPatientProvider(ctx context.Context, conn *substrate.Conn, apptKey string) (patientKey, providerKey string, found bool) {
	patientPrefix := "lnk." + strings.TrimPrefix(apptKey, "vtx.") + ".forPatient.patient."
	patientKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, patientPrefix)
	must(err, "list "+patientPrefix+" keys")
	for _, k := range patientKeys {
		if alive(ctx, conn, k) {
			patientKey = "vtx.patient." + strings.TrimPrefix(k, patientPrefix)
			break
		}
	}
	providerPrefix := "lnk." + strings.TrimPrefix(apptKey, "vtx.") + ".withProvider.provider."
	providerKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, providerPrefix)
	must(err, "list "+providerPrefix+" keys")
	for _, k := range providerKeys {
		if alive(ctx, conn, k) {
			providerKey = "vtx.provider." + strings.TrimPrefix(k, providerPrefix)
			break
		}
	}
	return patientKey, providerKey, patientKey != "" && providerKey != ""
}

// reapNonCanonicalPatients tombstones every live vtx.patient.* not in keep
// (verticals.md "the patient-facing site picker offers a verify artifact as
// a clinic": the roster carried 5 duplicate "Classic Demo Patient" rows from
// pre-pin reruns plus ad hoc verify-*.go/PO-discovery patients — "Grid Snap
// Test Patient", "Post-Merge Verify Patient", "Inc2a Live Verify Patient",
// "Steward Test Patient", "Retention Proof", "Verify Waiver Patient", live-
// grepped — none of which reap themselves). Allowlist by key, not
// isVerifyLitterName's name marker: "Grid Snap Test Patient" and "Retention
// Proof" contain neither "Verify" nor "Discovery", so a name sweep silently
// strands them (caught live) — the only two patient identities
// any checked-in seed script pins are patientID (this file) and
// rileyPatientID (seed-showcase.go), so anything else alive is litter by
// construction, not by guessing a broader name pattern.
func reapNonCanonicalPatients(ctx context.Context, conn *substrate.Conn, adminKey string, keep map[string]bool) {
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.patient.")
	must(err, "list vtx.patient. keys")
	for _, key := range keys {
		if strings.Count(key, ".") != 2 || keep[key] || !alive(ctx, conn, key) {
			continue
		}
		name, _ := readAspectFullName(ctx, conn, key+".demographics")
		submitOp(ctx, conn, adminKey, "TombstonePatient", "patient",
			map[string]any{"patientKey": key},
			&processor.ContextHint{Reads: []string{key}})
		fmt.Printf("==> reaped non-canonical patient: %s (%s)\n", key, name)
	}
}

// reapNonCanonicalSites tombstones every live vtx.building.* not in keep —
// same allowlist rationale as reapNonCanonicalPatients, applied to the site
// directory ("PO Discovery Test Site" live-grepped alongside the canonical
// "Riverside Clinic" seed-showcase.go mints). This script itself never
// creates a building (the clinic appointment above deliberately carries no
// site — see its comment), so every live vtx.building.* besides the one
// canonical id is a verify-fire building from elsewhere in the codebase.
func reapNonCanonicalSites(ctx context.Context, conn *substrate.Conn, adminKey string, keep map[string]bool) {
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.building.")
	must(err, "list vtx.building. keys")
	for _, key := range keys {
		if strings.Count(key, ".") != 2 || keep[key] || !alive(ctx, conn, key) {
			continue
		}
		name, _ := readAspectName(ctx, conn, key+".site")
		submitOp(ctx, conn, adminKey, "TombstoneLocation", "building",
			map[string]any{"locationKey": key},
			&processor.ContextHint{Reads: []string{key}})
		fmt.Printf("==> reaped non-canonical site: %s (%s)\n", key, name)
	}
}

// readAspectFullName reads a {isDeleted, data:{fullName}} aspect and returns
// its fullName when the aspect is alive — the patient .demographics shape,
// which carries fullName rather than readAspectName's name field.
func readAspectFullName(ctx context.Context, conn *substrate.Conn, aspectKey string) (string, bool) {
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, aspectKey)
	if err != nil {
		return "", false
	}
	var aspect struct {
		IsDeleted bool `json:"isDeleted"`
		Data      struct {
			FullName string `json:"fullName"`
		} `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &aspect); err != nil || aspect.IsDeleted {
		return "", false
	}
	return aspect.Data.FullName, true
}

// reapDuplicateStudios tombstones every live "Classic Demo Studio" other than
// the checked-in canonical one (keep) — CreateStudio's studioId has always
// been pinned by this script, but earlier reruns (before the id was derived
// deterministically) each minted a fresh vertex, so the booking picker and
// Studios tab still render every one of them (verticals.md). Name-filtered
// only, mirroring reapDuplicateProviders — safe because seed-showcase.go's
// own studio uses a different name ("Riverside Movement Studio").
func reapDuplicateStudios(ctx context.Context, conn *substrate.Conn, adminKey, keep string) {
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.studio.")
	must(err, "list vtx.studio. keys")
	for _, key := range keys {
		if key == keep || !alive(ctx, conn, key) {
			continue
		}
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key+".profile")
		if err != nil {
			continue
		}
		var aspect struct {
			IsDeleted bool `json:"isDeleted"`
			Data      struct {
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := json.Unmarshal(entry.Value, &aspect); err != nil || aspect.IsDeleted {
			continue
		}
		if aspect.Data.Name != "Classic Demo Studio" {
			continue
		}
		submitOp(ctx, conn, adminKey, "TombstoneStudio", "studio",
			map[string]any{"studioKey": key},
			&processor.ContextHint{Reads: []string{key}})
		fmt.Printf("==> reaped duplicate studio: %s (%s)\n", key, aspect.Data.Name)
	}
}

// isVerifyLitterName reports whether a display name follows this codebase's
// established ad hoc verify-fire naming convention ("PO Discovery Studio",
// "Steward Verify Studio", "Recurrence Verify Flow", …) rather than one of
// the seed scripts' own checked-in canonical names — none of which contain
// "Verify" or "Discovery" (grepped live: "Classic Demo Studio", "Riverside
// Movement Studio", "Vinyasa Flow", "Evening Flow with Sam").
func isVerifyLitterName(name string) bool {
	return strings.Contains(name, "Verify") || strings.Contains(name, "Discovery")
}

// reapVerifyLitter tombstones every session and studio a live-stack sweep
// identifies as verify-fire litter (verticals.md "the unauthenticated class
// schedule advertises agent-verify artifacts": ad hoc verify-*.go/PO-discovery
// runs across the codebase mint demo-visible studios/sessions and never reap
// them, so /api/studios + /api/sessions permanently render the litter). A
// studio is litter by its OWN name (isVerifyLitterName); a session is litter
// by its own name OR by running at a litter studio — the latter matters
// because TombstoneStudio deliberately doesn't cascade onto its sessions (own
// DDL doc), so a name-only session filter tombstones the studio out from
// under a plainly-named session ("Vinyasa Flow") and strands it with
// missingStudio=true instead of removing it (observed live: a name-only
// filter stranded 5 sessions this way).
// Every session is reaped before its studio, releasing held slot claims and
// — via wellnessOrphanedBookingSettlement, already-live Weaver convergence —
// any booking still anchored to it.
func reapVerifyLitter(ctx context.Context, conn *substrate.Conn, adminKey string) {
	studioKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.studio.")
	must(err, "list vtx.studio. keys")
	litterStudios := map[string]string{}
	for _, key := range studioKeys {
		if strings.Count(key, ".") != 2 || !alive(ctx, conn, key) {
			continue
		}
		if name, ok := readAspectName(ctx, conn, key+".profile"); ok && isVerifyLitterName(name) {
			litterStudios[key] = name
		}
	}

	sessionKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.session.")
	must(err, "list vtx.session. keys")
	for _, key := range sessionKeys {
		if strings.Count(key, ".") != 2 || !alive(ctx, conn, key) {
			continue
		}
		name, ok := readAspectName(ctx, conn, key+".schedule")
		if !ok {
			continue
		}
		studioKey, foundStudio := findSessionStudio(ctx, conn, key)
		if !foundStudio {
			continue
		}
		_, atLitterStudio := litterStudios[studioKey]
		if !isVerifyLitterName(name) && !atLitterStudio {
			continue
		}
		submitOp(ctx, conn, adminKey, "TombstoneSession", "session",
			map[string]any{"sessionKey": key, "studio": studioKey},
			&processor.ContextHint{Reads: []string{key, studioKey}})
		fmt.Printf("==> reaped verify-litter session: %s (%s)\n", key, name)
	}

	studioKeysSorted := make([]string, 0, len(litterStudios))
	for key := range litterStudios {
		studioKeysSorted = append(studioKeysSorted, key)
	}
	sort.Strings(studioKeysSorted)
	for _, key := range studioKeysSorted {
		submitOp(ctx, conn, adminKey, "TombstoneStudio", "studio",
			map[string]any{"studioKey": key},
			&processor.ContextHint{Reads: []string{key}})
		fmt.Printf("==> reaped verify-litter studio: %s (%s)\n", key, litterStudios[key])
	}
}

// findSessionStudio returns the studio key a session's live atStudio link
// names, mirroring findManagingLandlord's prefix-scan shape (the link's own
// studio suffix isn't known up front, so it can't be constructed directly).
func findSessionStudio(ctx context.Context, conn *substrate.Conn, sessionKey string) (string, bool) {
	prefix := "lnk." + strings.TrimPrefix(sessionKey, "vtx.") + ".atStudio.studio."
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, prefix)
	must(err, "list "+prefix+" keys")
	for _, key := range keys {
		if alive(ctx, conn, key) {
			return "vtx.studio." + strings.TrimPrefix(key, prefix), true
		}
	}
	return "", false
}

// readAspectName reads a {isDeleted, data:{name}} aspect and returns its name
// when the aspect is alive — shared by reapVerifyLitter for both the studio
// .profile and session .schedule aspect shapes, which both carry a name field.
func readAspectName(ctx context.Context, conn *substrate.Conn, aspectKey string) (string, bool) {
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, aspectKey)
	if err != nil {
		return "", false
	}
	var aspect struct {
		IsDeleted bool `json:"isDeleted"`
		Data      struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &aspect); err != nil || aspect.IsDeleted {
		return "", false
	}
	return aspect.Data.Name, true
}

// findManagingLandlord returns the identity key of any live landlord already
// holding a `manages` link to unitKey, mirroring seed-showcase.go's
// ensureLandlord/findLinkedIdentity convergence check (this script is a
// standalone `go run` file, so the helper is re-derived rather than shared).
// Prefix-scans lnk.identity. rather than constructing the link key directly —
// unlike unitID/studioID, the landlord's own NanoID is never client-pinned
// (CreateUnclaimedIdentity mints it), so the source id isn't known up front.
func findManagingLandlord(ctx context.Context, conn *substrate.Conn, unitKey string) (string, bool) {
	suffix := ".manages." + strings.TrimPrefix(unitKey, "vtx.")
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "lnk.identity.")
	must(err, "list lnk.identity. keys")
	sort.Strings(keys)
	for _, key := range keys {
		if !strings.HasSuffix(key, suffix) || !alive(ctx, conn, key) {
			continue
		}
		return "vtx.identity." + landlordIDFromManagesLink(key), true
	}
	return "", false
}

// landlordIDFromManagesLink extracts <landlordID> from a
// lnk.identity.<landlordID>.manages.unit.<unitID> key.
func landlordIDFromManagesLink(key string) string {
	rest := strings.TrimPrefix(key, "lnk.identity.")
	return rest[:strings.Index(rest, ".manages.")]
}

// reapExcessCoManagers removes every live `manages` link on unitKey other
// than keepLandlordKey. findManagingLandlord stops a future rerun from
// minting a new co-manager, but does not by itself undo the ones a decade of
// unguarded prior runs already wired (10 accrued on the pinned unit,
// verticals.md) — RemoveUnitOwner is granted to operator unconditionally
// (loftspace-domain/ownership.go's enforce_manages exempts the operator
// role), so adminKey can drop a co-manager it never created.
func reapExcessCoManagers(ctx context.Context, conn *substrate.Conn, adminKey, unitKey, keepLandlordKey string) {
	suffix := ".manages." + strings.TrimPrefix(unitKey, "vtx.")
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "lnk.identity.")
	must(err, "list lnk.identity. keys")
	for _, key := range keys {
		if !strings.HasSuffix(key, suffix) || !alive(ctx, conn, key) {
			continue
		}
		landlordKey := "vtx.identity." + landlordIDFromManagesLink(key)
		if landlordKey == keepLandlordKey {
			continue
		}
		submitOp(ctx, conn, adminKey, "RemoveUnitOwner", "loftspaceOwnership",
			map[string]any{"landlord": landlordKey, "unit": unitKey},
			&processor.ContextHint{
				Reads:         []string{landlordKey, unitKey},
				OptionalReads: []string{linkKey(landlordKey, "manages", unitKey)},
			})
		fmt.Printf("==> reaped excess co-manager: %s no longer manages %s\n", landlordKey, unitKey)
	}
}

// reapExcessCoManagersLive generalizes reapExcessCoManagers past the seed's
// own pinned unit: it scans every live `manages` link on Core KV, groups by
// target unit, and on any unit carrying more than one keeps only the
// alphabetically-first landlord key — the same canonicality findManagingLandlord
// already uses (NanoIDs sort by creation order, so this keeps the oldest).
// By the time this runs the pinned unit is already narrowed to keepLandlordKey
// by the call above, so this is a no-op there; every OTHER live unit that
// accrued co-managers across a decade of unguarded prior runs (12 Riverside
// Walk carried 7, verticals.md) converges here instead.
func reapExcessCoManagersLive(ctx context.Context, conn *substrate.Conn, adminKey string) {
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "lnk.identity.")
	must(err, "list lnk.identity. keys")
	byUnit := map[string][]string{}
	for _, key := range keys {
		idx := strings.Index(key, ".manages.unit.")
		if idx < 0 || !alive(ctx, conn, key) {
			continue
		}
		unitID := key[idx+len(".manages.unit."):]
		byUnit[unitID] = append(byUnit[unitID], key)
	}
	for unitID, links := range byUnit {
		if len(links) < 2 {
			continue
		}
		sort.Strings(links)
		unitKey := "vtx.unit." + unitID
		for _, key := range links[1:] {
			landlordKey := "vtx.identity." + landlordIDFromManagesLink(key)
			submitOp(ctx, conn, adminKey, "RemoveUnitOwner", "loftspaceOwnership",
				map[string]any{"landlord": landlordKey, "unit": unitKey},
				&processor.ContextHint{
					Reads:         []string{landlordKey, unitKey},
					OptionalReads: []string{linkKey(landlordKey, "manages", unitKey)},
				})
			fmt.Printf("==> reaped excess co-manager: %s no longer manages %s\n", landlordKey, unitKey)
		}
	}
}

// reapDuplicateListings tombstones every live unit at "12 Classic Demo Ave"
// other than the checked-in canonical one (keep) — unitID has always been
// pinned by this script mirroring reapDuplicateProviders/reapDuplicateStudios,
// but earlier reruns (before the id was pinned) each minted a fresh unit +
// address + listing, so LoftSpace's browsable inventory still renders every
// one of them, several with no manages landlord at all (undecidable —
// permissions.go's DecideLeaseApplication self-scope requires one;
// verticals.md). Address-filtered rather than name-filtered — CreateLocation's
// presentation.name here is the generic "Unit 1" — mirroring
// reapDuplicateProviders' safety rationale: no other seed script mints a unit
// at this address.
func reapDuplicateListings(ctx context.Context, conn *substrate.Conn, adminKey, keep string) {
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.unit.")
	must(err, "list vtx.unit. keys")
	for _, key := range keys {
		if key == keep || !alive(ctx, conn, key) {
			continue
		}
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key+".address")
		if err != nil {
			continue
		}
		var aspect struct {
			IsDeleted bool `json:"isDeleted"`
			Data      struct {
				Line1 string `json:"line1"`
			} `json:"data"`
		}
		if err := json.Unmarshal(entry.Value, &aspect); err != nil || aspect.IsDeleted {
			continue
		}
		if aspect.Data.Line1 != "12 Classic Demo Ave" {
			continue
		}
		submitOp(ctx, conn, adminKey, "TombstoneLocation", "unit",
			map[string]any{"locationKey": key},
			&processor.ContextHint{Reads: []string{key}})
		fmt.Printf("==> reaped duplicate listing: %s (12 Classic Demo Ave)\n", key)
	}
}

// ensureLandlord converges unitKey onto a live `manages` link, minting a new
// unclaimed landlord identity only if none is found. Mirrors
// seed-showcase.go's seedLandlord/ensureLandlord — CreateUnclaimedIdentity
// mints a fresh vtx.identity on every call (no client-supplied id, unlike
// CreateLocation's locationId), so a landlord can't be pinned by const the
// way unitID/studioID are; findManagingLandlord converges on the `manages`
// link itself instead, so a rerun never mints and co-manages a second
// landlord onto a unit that already has one.
func ensureLandlord(ctx context.Context, conn *substrate.Conn, adminKey, unitKey, consumerRoleKey string) string {
	if landlordKey, found := findManagingLandlord(ctx, conn, unitKey); found {
		return landlordKey
	}
	landlordSalt, err := substrate.NewNanoID()
	must(err, "generate landlord email salt")
	landlordReply := submitOp(ctx, conn, adminKey, "CreateUnclaimedIdentity", "identity",
		map[string]any{
			"name":         "Classic Demo Landlord " + landlordSalt[:8],
			"email":        "landlord-" + landlordSalt[:8] + "@dev.lattice.local",
			"claimKeyHash": mustSHA256Hex("classic-demo-landlord-" + landlordSalt),
		}, nil)
	landlordKey := landlordReply.PrimaryKey
	submitOp(ctx, conn, adminKey, "AssignRole", "",
		map[string]any{"actorKey": landlordKey, "roleKey": consumerRoleKey},
		&processor.ContextHint{Reads: []string{landlordKey, consumerRoleKey}})
	submitOp(ctx, conn, adminKey, "AssignUnitOwner", "loftspaceOwnership",
		map[string]any{"landlord": landlordKey, "unit": unitKey},
		&processor.ContextHint{
			Reads:         []string{landlordKey, unitKey},
			OptionalReads: []string{linkKey(landlordKey, "manages", unitKey)},
		})
	return landlordKey
}

// backfillUnownedListings gives every live vtx.unit.* that already has a
// `.listing` aspect but no live `manages` link a landlord (verticals.md
// "Every unit an applicant can browse is unowned, so no landlord can ever
// decide the application"): DecideLeaseApplication's landlord path walks
// appliesToUnit -> manages, so a listed-but-unowned unit's applications can
// only ever be decided by an operator, never a landlord. Runs after
// backfillBareListings so a unit it just gave a listing to is covered in the
// same pass.
func backfillUnownedListings(ctx context.Context, conn *substrate.Conn, adminKey, consumerRoleKey string) {
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.unit.")
	must(err, "list vtx.unit. keys")
	sort.Strings(keys)
	for _, key := range keys {
		if strings.Count(key, ".") != 2 || !alive(ctx, conn, key) || !alive(ctx, conn, key+".listing") {
			continue
		}
		if _, found := findManagingLandlord(ctx, conn, key); found {
			continue
		}
		landlordKey := ensureLandlord(ctx, conn, adminKey, key, consumerRoleKey)
		fmt.Printf("==> backfilled owner: %s manages %s\n", landlordKey, key)
	}
}

// backfillBareListings gives every live vtx.unit.* still missing a `.listing`
// aspect an address + an available listing (verticals.md "The applicant
// storefront is empty and cannot refill"). This script only ever manages its
// one pinned unitID, so once that unit's lease is signed (status flips to
// "leased"), /api/listings has nothing left to show — units minted by
// earlier seed/verify runs sit alive in Core KV with no address or listing
// at all, permanently unlistable even though they're real vacant inventory.
// Filtered to root unit keys only (exactly two dots, `vtx.unit.<id>`) so a
// unit's own `.address`/`.listing`/`.presentation` sub-keys, which also carry
// the "vtx.unit." prefix, aren't mistaken for separate units.
func backfillBareListings(ctx context.Context, conn *substrate.Conn, adminKey string) {
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.unit.")
	must(err, "list vtx.unit. keys")
	sort.Strings(keys)
	for i, key := range keys {
		if strings.Count(key, ".") != 2 || !alive(ctx, conn, key) || alive(ctx, conn, key+".listing") {
			continue
		}
		submitOp(ctx, conn, adminKey, "SetUnitAddress", "loftspaceListing",
			map[string]any{"unit": key, "line1": fmt.Sprintf("%d Backfill Ave", i+1),
				"city": "Springfield", "region": "OR", "postal": "97477"},
			&processor.ContextHint{Reads: []string{key}})
		submitOp(ctx, conn, adminKey, "SetListing", "loftspaceListing",
			map[string]any{"unit": key, "rentAmount": 1800, "rentCurrency": "USD", "bedrooms": 1,
				"availableFrom": time.Now().UTC().Format(time.RFC3339), "leaseTermMonths": 12, "status": "available"},
			&processor.ContextHint{Reads: []string{key}})
		fmt.Printf("==> backfilled listing: %s (available)\n", key)
	}
}

// backfillCafeFrontDeskBadges books a fresh forward-dated wellness class and
// clinic visit for one already-tenanted resident so the café front desk's two
// P5 lens buckets (front-desk-bookings, front-desk-visits —
// packages/front-desk/lenses.go) carry at least one live row each
// (verticals.md "Every resident's class + visit badge on the café front desk
// is blank"): live-stack census found every existing residentRate booking and
// residentVisit appointment had already gone noShow or been tombstoned, so
// the front-desk "class + visit badge" composition surface rendered nothing
// even though the underlying confinement plumbing is sound.
//
// Rather than mint a brand-new resident/lease/patient world, this converges
// on the FIRST live leaseapp already carrying a signed tenancy (a live
// .tenancy aspect — CreateOnly, stamped on a lease's first
// DecideLeaseApplication approve) whose applicationFor identity is ALSO the
// identifiedBy target of some already-live clinic patient — the exact
// precondition wellness-domain's CreateBooking and clinic-domain's
// CreateAppointment each check before writing a residentRate / residentVisit
// link (packages/wellness-domain/ddls.go prepare_booking_common;
// packages/clinic-domain/ddls.go's CreateAppointment resident-visit branch).
// The booking targets the wellness session this file's own Wellness section
// just ensured is live and forward-dated (sessionKey); the visit picks its
// own day-derived slot against the canonical provider, walked forward a few
// days if the resident or provider already holds a claim there.
func backfillCafeFrontDeskBadges(ctx context.Context, conn *substrate.Conn, adminKey, sessionKey, providerKey string) {
	leaseKey, patientKey, identityKey, found := findTenantedLeaseWithLinkedPatient(ctx, conn)
	if !found {
		fmt.Println("==> café front-desk badges: no live tenanted-lease + identified-patient resident found, skipping")
		return
	}
	fmt.Printf("==> front-desk resident: %s (lease %s, patient %s)\n", identityKey, leaseKey, patientKey)

	_, bookerID, _ := substrate.ParseVertexKey(identityKey)
	_, leaseID, _ := substrate.ParseVertexKey(leaseKey)
	bookID := substrate.DeriveNanoID("cafe-frontdesk-badge-booking", sessionKey)
	bookKey := "vtx.booking." + bookID
	if !alive(ctx, conn, bookKey) {
		submitOp(ctx, conn, adminKey, "CreateBooking", "booking",
			map[string]any{"session": sessionKey, "booker": identityKey, "leaseAppKey": leaseKey, "bookingId": bookID},
			&processor.ContextHint{
				Reads: []string{sessionKey, sessionKey + ".schedule", identityKey},
				OptionalReads: append(bookingSeatKeys(sessionKey, 20),
					sessionKey+".bkr"+bookerID,
					leaseKey, leaseKey+".tenancy",
					"lnk.leaseapp."+leaseID+".applicationFor.identity."+bookerID),
			})
	}
	fmt.Printf("==> booking:         %s (residentRate, session %s)\n", bookKey, sessionKey)

	start, end, foundSlot := findOpenApptSlot(ctx, conn, providerKey, patientKey)
	if !foundSlot {
		fmt.Println("==> café front-desk badges: no open appointment slot found for the resident visit, skipping visit backfill")
		return
	}
	apptID := substrate.DeriveNanoID("cafe-frontdesk-badge-visit", start.Format("2006-01-02"))
	apptKey := "vtx.appointment." + apptID
	if !alive(ctx, conn, apptKey) {
		submitOp(ctx, conn, adminKey, "CreateAppointment", "appointment",
			map[string]any{
				"patient": patientKey, "provider": providerKey, "appointmentId": apptID,
				"startsAt": start.Format(time.RFC3339), "endsAt": end.Format(time.RFC3339),
				"reason": "Front desk badge check", "leaseAppKey": leaseKey,
			},
			&processor.ContextHint{
				Reads: []string{patientKey, providerKey},
				OptionalReads: append(
					append(slotClaimKeys(providerKey, start, end), slotClaimKeys(patientKey, start, end)...),
					leaseKey, leaseKey+".tenancy"),
			})
	}
	fmt.Printf("==> visit:           %s (residentVisit, %s)\n", apptKey, start.Format(time.RFC3339))
}

// findTenantedLeaseWithLinkedPatient scans live vtx.leaseapp.* root vertices
// for one carrying a live .tenancy aspect (an approved, signed lease, not
// merely applied) whose applicationFor identity is ALSO the identifiedBy
// target of some already-live clinic patient — the precise resident-rate /
// resident-visit precondition (see backfillCafeFrontDeskBadges' doc comment).
// Sorted so the choice is deterministic across reruns on an unchanged stack.
// Direct Core KV reads, sanctioned here for the same reason every other reap/
// backfill helper in this file uses them (dev/ops loader, not a P5 read path).
func findTenantedLeaseWithLinkedPatient(ctx context.Context, conn *substrate.Conn) (leaseKey, patientKey, identityKey string, found bool) {
	leaseKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.leaseapp.")
	must(err, "list vtx.leaseapp. keys")
	sort.Strings(leaseKeys)
	for _, lk := range leaseKeys {
		if strings.Count(lk, ".") != 2 || !alive(ctx, conn, lk) || !alive(ctx, conn, lk+".tenancy") {
			continue
		}
		leaseID := strings.TrimPrefix(lk, "vtx.leaseapp.")
		appForPrefix := "lnk.leaseapp." + leaseID + ".applicationFor.identity."
		appForKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, appForPrefix)
		must(err, "list "+appForPrefix+" keys")
		var identID string
		for _, k := range appForKeys {
			if alive(ctx, conn, k) {
				identID = strings.TrimPrefix(k, appForPrefix)
				break
			}
		}
		if identID == "" {
			continue
		}

		patSuffix := ".identifiedBy.identity." + identID
		patKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "lnk.patient.")
		must(err, "list lnk.patient. keys")
		sort.Strings(patKeys)
		for _, pk := range patKeys {
			if !strings.HasSuffix(pk, patSuffix) || !alive(ctx, conn, pk) {
				continue
			}
			patID := strings.TrimSuffix(strings.TrimPrefix(pk, "lnk.patient."), patSuffix)
			candidatePatientKey := "vtx.patient." + patID
			if !alive(ctx, conn, candidatePatientKey) {
				continue
			}
			return lk, candidatePatientKey, "vtx.identity." + identID, true
		}
	}
	return "", "", "", false
}

// findOpenApptSlot walks a handful of day-offsets 30+ days out (well clear of
// every other date this file's own idioms derive appointments/sessions at,
// which stay within a day or two of time.Now()) looking for a 30-minute,
// 15-minute-grid-aligned slot where NEITHER providerKey nor patientKey
// already holds a slot claim — clinic-domain's CreateAppointment rejects a
// covered cell either side already holds with SlotConflict/PatientDoubleBook.
// Fixed at 10:00 UTC, comfortably inside the canonical provider's 08:00-18:00
// hours (SetProviderHours above).
func findOpenApptSlot(ctx context.Context, conn *substrate.Conn, providerKey, patientKey string) (start, end time.Time, found bool) {
	for _, days := range []int{30, 31, 32, 33, 34, 35, 36, 37, 38, 39} {
		day := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour).Truncate(24 * time.Hour)
		candidateStart := day.Add(10 * time.Hour)
		candidateEnd := candidateStart.Add(30 * time.Minute)
		occupied := false
		for _, key := range append(slotClaimKeys(providerKey, candidateStart, candidateEnd), slotClaimKeys(patientKey, candidateStart, candidateEnd)...) {
			if alive(ctx, conn, key) {
				occupied = true
				break
			}
		}
		if !occupied {
			return candidateStart, candidateEnd, true
		}
	}
	return time.Time{}, time.Time{}, false
}

// bookingSeatKeys enumerates a session's seat-claim aspect keys 1..n,
// mirroring wellness-domain integration_test.go's wdSeatKeys (claim_first_free_seat's
// bounded loop, ddls.go) — n=20 covers this file's own session capacity (12)
// with headroom, the same fixed bound that suite's createBooking helper uses.
func bookingSeatKeys(sessionKey string, n int) []string {
	keys := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		keys = append(keys, sessionKey+".seat"+fmt.Sprint(i))
	}
	return keys
}

// reapGhostLeases withdraws every live leaseapp whose applicant is the
// bootstrap admin identity itself — verify-fire litter (verticals.md:
// verify-staff-write-confinement.go mints a fresh one per run with
// "applicant": admin and no cleanup step of its own), never a real resident.
// cafeIdentitiesReadSpec (cafe-domain/lenses.go) correctly excludes an
// unnamed identity (its WHERE requires a `.name` aspect), so a lease held by
// admin renders as a raw shortKey in the café POS lease picker instead of
// dropping out, and its verify-only unit — minted by CreateLocation alone,
// never given a `.listing` via SetUnitAddress/SetListing — reads blank
// address / $0 rent in frontDeskLeaseDetails. Generic over the admin's bare
// ID rather than two hardcoded lease keys, so any future admin-applicant
// litter (this script or another verify tool) is caught the same way.
func reapGhostLeases(ctx context.Context, conn *substrate.Conn, adminKey string) {
	reapLeasesByApplicant(ctx, conn, adminKey)
}

// reapVerifyTenantLease withdraws the one leaseapp a "LandlordKey"-scoped
// verify fire left applied for by a fresh throwaway identity of its own
// minting (never the admin — reapGhostLeases' pattern doesn't match this
// one), which surfaced in the café front-desk resident roster
// (verticals.md "A verify artifact holds a lease in the café front-desk
// roster") the same way an admin-applicant ghost lease would. Named by the
// one instance a live PO drive found rather than a name-pattern sweep: the
// applicant identity's `.name` aspect is privacy-encrypted, so this script
// (a bare Core KV scanner, no decrypt path) cannot resolve "starts with
// Verify" itself the way reapNonCanonicalPatients/reapVerifyLitter compare
// plaintext demographic/session names.
func reapVerifyTenantLease(ctx context.Context, conn *substrate.Conn) {
	reapLeasesByApplicant(ctx, conn, "vtx.identity.4zt8PDpgwUQYqUYPXZeb")
}

// reapLeasesByApplicant withdraws every live leaseapp whose applicant is
// applicantKey, submitting WithdrawLeaseApplication as that same identity
// (the consumer scope=self grant — the applicant withdrawing its own
// application — never scope=any, since a verify-litter applicant holds no
// operator role).
func reapLeasesByApplicant(ctx context.Context, conn *substrate.Conn, applicantKey string) {
	applicantID := strings.TrimPrefix(applicantKey, "vtx.identity.")
	appForSuffix := ".applicationFor.identity." + applicantID
	links, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "lnk.leaseapp.")
	must(err, "list lnk.leaseapp. keys")
	for _, link := range links {
		if !strings.HasSuffix(link, appForSuffix) || !alive(ctx, conn, link) {
			continue
		}
		leaseID := strings.TrimSuffix(strings.TrimPrefix(link, "lnk.leaseapp."), appForSuffix)
		leaseKey := "vtx.leaseapp." + leaseID
		if !alive(ctx, conn, leaseKey) {
			continue
		}
		unitPrefix := "lnk.leaseapp." + leaseID + ".appliesToUnit.unit."
		unitLinks, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, unitPrefix)
		if err != nil || len(unitLinks) == 0 || !alive(ctx, conn, unitLinks[0]) {
			continue
		}
		unitID := strings.TrimPrefix(unitLinks[0], unitPrefix)
		unitKey := "vtx.unit." + unitID
		submitSelfOp(ctx, conn, applicantKey, "WithdrawLeaseApplication", "leaseapp",
			map[string]any{"leaseAppKey": leaseKey, "unit": unitKey, "applicant": applicantKey},
			&processor.ContextHint{
				Reads: []string{
					leaseKey,
					unitPrefix + unitID,
					link,
				},
				OptionalReads: []string{"lnk.identity." + applicantID + ".appliedToUnit.unit." + unitID},
			})
		fmt.Printf("==> reaped ghost lease: %s (applicant=%s, verify-fire litter)\n", leaseKey, applicantKey)
	}
}

// alive reports whether key names a live (non-tombstoned) Core KV document —
// a direct Core KV read, sanctioned here only because this is a dev/ops
// loader tool, not a P5 vertical-app read path (mirrors seed-showcase.go's
// helper of the same name).
func alive(ctx context.Context, conn *substrate.Conn, key string) bool {
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
	if err != nil {
		return false
	}
	var doc struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return false
	}
	return !doc.IsDeleted
}

// submitOp submits an operation as actorKey over NATS (the bootstrap-actor
// setup path, not the Gateway) and fatals on a transport error or a rejected
// reply, mirroring seed-edge-demo.go's helper of the same name.
func submitOp(ctx context.Context, conn *substrate.Conn, actorKey, operationType, class string, payload map[string]any, hint *processor.ContextHint) *processor.OperationReply {
	reqID, err := substrate.NewNanoID()
	must(err, "generate requestId")
	payloadBytes, err := json.Marshal(payload)
	must(err, "marshal payload")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: operationType,
		Actor:         actorKey,
		Class:         class,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Payload:       payloadBytes,
		ContextHint:   hint,
	}
	reply, err := output.SubmitOp(ctx, conn, env)
	must(err, "submit "+operationType)
	mustAccepted(reply, operationType)
	return reply
}

// submitSelfOp is submitOp for a platform scope=self grant, where
// authContext.target must equal the acting identity itself (Contract #6) —
// the "withdraw my own application" shape, as opposed to submitOp's
// operator/scope=any actions.
func submitSelfOp(ctx context.Context, conn *substrate.Conn, actorKey, operationType, class string, payload map[string]any, hint *processor.ContextHint) *processor.OperationReply {
	reqID, err := substrate.NewNanoID()
	must(err, "generate requestId")
	payloadBytes, err := json.Marshal(payload)
	must(err, "marshal payload")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: operationType,
		Actor:         actorKey,
		Class:         class,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Payload:       payloadBytes,
		ContextHint:   hint,
		AuthContext:   &processor.AuthContext{Target: actorKey},
	}
	reply, err := output.SubmitOp(ctx, conn, env)
	must(err, "submit "+operationType)
	mustAccepted(reply, operationType)
	return reply
}

func mustAccepted(reply *processor.OperationReply, context string) {
	if reply.Status == processor.ReplyStatusAccepted {
		return
	}
	if reply.Error != nil {
		fmt.Fprintf(os.Stderr, "FATAL %s: rejected code=%s message=%s\n", context, reply.Error.Code, reply.Error.Message)
	} else {
		fmt.Fprintf(os.Stderr, "FATAL %s: status=%s (no error detail)\n", context, reply.Status)
	}
	os.Exit(1)
}

func must(err error, context string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL %s: %v\n", context, err)
		os.Exit(1)
	}
}

func mustSHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
