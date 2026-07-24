//go:build ignore

// seed-showcase.go — dev-seed for `make seed-showcase` (edge-showcase-app-
// design.md §7.3, §7.4, §7.6): loads a curated two-persona demo world using
// service-domain's real families ({laundry, fitness, clinic, wellness, cafe}
// among the enum) — P7 makes the envelope class the machine truth, so a template's
// family must match what it's presented as, or a completed run of it reads
// as a different real thing to any family-matching consumer (e.g.
// lease-signing's renewals lens).
//
// IDEMPOTENT (deterministic handles so reloads converge, unlike
// seed-edge-demo.go's "fresh vertices every run"): every location/template
// vertex is minted with a FIXED, checked-in bare NanoID (Contract #1 — valid
// 20-char canonical alphabet, internal/substrate/nanoid.go). The showcase
// building is the idempotency anchor: if it already exists (alive), the
// whole world is assumed already loaded — this run recovers + prints the two
// tenant identity keys (found by scanning residesIn links into the two fixed
// units — the only way to recover a MINTED identity id, since
// CreateUnclaimedIdentity accepts no caller-supplied id) and exits WITHOUT
// re-submitting a single op. Tenant identities are therefore the one entity
// this loader cannot itself dedup on a from-scratch rerun after a manual
// tombstone of the building alone — not a concern for its actual use (the
// building is never tombstoned independently).
//
// Every mutation is an ordinary operation through NATS as the bootstrap
// admin actor (P2 — never a direct KV write; mirrors seed-edge-demo.go /
// dev-seed-staff). The two reads (the building idempotency check, and the
// legacy-template alive-check below) are direct Core KV reads — a dev/ops
// loader tool, not a P5 vertical-app read path (same posture as
// scripts/seed-edge-demo.go's own findRequestServiceOpMeta and
// waitForRoleGrant helpers).
//
// Requires `make install-edge-manifest` already applied to the target stack
// (service-domain + edge-manifest installed). Run via:
// `make seed-showcase` (== go run ./scripts/seed-showcase.go).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/capabilitykv"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/scripts/pkgverify"
)

// Fixed, checked-in handles (Contract #1 — valid 20-char canonical NanoIDs,
// generated once via substrate.NewNanoID and pinned here) — the idempotency
// seam: a second run recognizes this exact world instead of minting a
// disjoint one.
const (
	buildingID    = "A9jnKK2bGwZNrfHHkLme"
	unit1ID       = "J11XtyS84Tiv16GcC6eE"
	unit2ID       = "eM2RNxs5S5rDFr6i8cfa"
	laundryTplID  = "z1vfcNcXdkdyHhJoFz55"
	fitnessTplID  = "xeY6h9HU3MYWUuiUZhcA"
	clinicTplID   = "hqYJYTcdwtPPfD2pPG8c"
	wellnessTplID = "nh8YmMryPJbSzhCyTLxR"
	cafeTplID     = "7HRxY1Ymcjv2kuWoR1uC"
	instance1ID   = "w3wX6tCr9EQMDo7zKu6P"
	studioID      = "aZmZkW2ws3mUHhRnWTJL"
	providerID    = "fkCFqiGUn5t9En8hoCrc"

	// CreateLocation mints vtx.<locationType>.<id> — the type-specific prefix,
	// not a generic "location" one (location-domain's ddls.go).
	buildingKey    = "vtx.building." + buildingID
	unit1Key       = "vtx.unit." + unit1ID
	unit2Key       = "vtx.unit." + unit2ID
	laundryTplKey  = "vtx.service." + laundryTplID
	fitnessTplKey  = "vtx.service." + fitnessTplID
	clinicTplKey   = "vtx.service." + clinicTplID
	wellnessTplKey = "vtx.service." + wellnessTplID
	cafeTplKey     = "vtx.service." + cafeTplID
	studioKey      = "vtx.studio." + studioID
	providerKey    = "vtx.provider." + providerID

	tenant1Name  = "Riley Chen"
	tenant1Email = "riley.chen@showcase.dev.lattice.local"
	tenant2Name  = "Sam Okafor"
	tenant2Email = "sam.okafor@showcase.dev.lattice.local"
	staffName    = "Dana Whitfield"
	staffEmail   = "dana.whitfield@showcase.dev.lattice.local"
	maintName    = "Theo Marsh"
	maintEmail   = "theo.marsh@showcase.dev.lattice.local"

	// The staff-worklist beat: a vacant third unit + a walk-in applicant whose
	// signed lease application sits undecided on the staff persona's worklist
	// (DecideLeaseApplication is frontOfHouse's own verb). The leaseapp id is
	// caller-supplied, so it is this increment's idempotency anchor — the
	// applicant identity is minted and never needs recovering.
	unit3ID         = "aCEFf63f9K6tR7eGnJ69"
	leaseApp3ID     = "pbCxpGRQHx9V23TZaC6H"
	unit3Key        = "vtx.unit." + unit3ID
	leaseApp3Key    = "vtx.leaseapp." + leaseApp3ID
	applicant3Name  = "Alex Kim"
	applicant3Email = "alex.kim@showcase.dev.lattice.local"
)

// The W0 provider-spine increment (persona-worlds-design.md Fire W0): a
// second, BOUND clinic provider (Dr. Amara Osei — Dr. Maya Patel, providerID
// above, stays deliberately UNBOUND as the scoping negative), a clinic
// patient record for tenant1 with a future appointment against each
// provider, a bound laundry serviceprovider (Kai) with one OPEN instance,
// and a wellness instructor entity binding tenant2 (Sam Okafor) into the
// §3.4 one-human-many-hats scenario. Fixed, checked-in handles like the
// block above — every entity id this loader mints itself is pinned; the
// identities BOUND to them stay minted-and-recovered, never fixed, exactly
// like every other persona in this file (CreateUnclaimedIdentity accepts no
// caller-supplied id).
const (
	oseiProviderID       = "6gerUBMpr5voBfo3dbS7"
	rileyPatientID       = "w5sDPrw4eraPfUHk96wo"
	kaiServiceProviderID = "JMzG4F5ierfew7ZvBkym"
	kaiInstanceID        = "EVc2u9YLmmZd8sqs1Kkn"
	samInstructorID      = "CYsYdVQ6unntMoHYffTF"

	oseiProviderKey       = "vtx.provider." + oseiProviderID
	rileyPatientKey       = "vtx.patient." + rileyPatientID
	kaiServiceProviderKey = "vtx.serviceprovider." + kaiServiceProviderID
	kaiInstanceKey        = "vtx.service." + kaiInstanceID
	samInstructorKey      = "vtx.instructor." + samInstructorID

	oseiName        = "Dr. Amara Osei"
	oseiEmail       = "amara.osei@showcase.dev.lattice.local"
	kaiBusinessName = "Kai's Laundry Co."
	kaiName         = "Kai"
	kaiEmail        = "kai@showcase.dev.lattice.local"
)

// showcaseLocationNames is the class-2 display copy (display-name-convention-
// design.md D2) for the three showcase locations, shared by the from-scratch
// CreateLocation path and the live-world SetLocationPresentation path so the
// two can never drift.
var showcaseLocationNames = map[string]map[string]any{
	buildingKey: {"name": "Riverside Building", "icon": "building"},
	unit1Key:    {"name": "Unit 1", "icon": "door"},
	unit2Key:    {"name": "Unit 2", "icon": "door"},
	unit3Key:    {"name": "Unit 3", "icon": "door"},
}

// showcaseLocationOrder fixes the iteration order a map does not have, so a
// rerun submits its ops — and prints its lines — deterministically.
var showcaseLocationOrder = []string{buildingKey, unit1Key, unit2Key, unit3Key}

// legacyMislabeledTemplates are the two backgroundCheck-classed templates
// seed-edge-demo.go minted, branded "Maple Laundry" via .presentation — §7.3
// names them explicitly for retirement. Best-effort: retired only if still
// alive (a stack that never ran the old seed never has them).
var legacyMislabeledTemplates = []string{
	"vtx.service.LWFqbYGKUErL34AidEEk",
	"vtx.service.UbwdojE6jBRQF31vwJjx",
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	natsURL := pkgverify.EnvOrDefault("NATS_URL", "nats://localhost:4222")
	bootstrapPath := pkgverify.EnvOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")

	must(bootstrap.Load(bootstrapPath), "load bootstrap JSON")

	conn, err := output.Connect(ctx, natsURL)
	must(err, "connect to NATS")
	defer conn.Close()

	adminKey := bootstrap.BootstrapIdentityKey
	// Computed once, up front, so both branches below can use them — the
	// already-loaded branch needs consumerRoleKey for ensureStaff/
	// ensureMaintenanceTech's hardening (a second frontOfHouse/backOfHouse
	// holder who ALSO holds consumer must never mis-resolve as the canonical
	// staff/maintenance persona) and providerRoleKey for the W0 provider
	// binds, exactly as much as the from-scratch branch does.
	consumerRoleKey := "vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")
	providerRoleKey := "vtx.role." + pkgmgr.RoleID("identity-domain", "provider")

	if alive(ctx, conn, buildingKey) {
		fmt.Println("==> showcase world already loaded (building", buildingKey, "is alive) — recovering persona keys, layering in any missing increment.")
		tenant1Key, tenant2Key := recoverTenants(ctx, conn, adminKey)
		retireLegacyTemplates(ctx, conn)
		// The building's liveness only proves the ORIGINAL world loaded; a
		// later increment (e.g. the §7.4 clinic template, the wellness
		// template below) can still be missing — or partially wired — on an
		// already-seeded stack. seedClinicTemplate/seedWellnessTemplate are
		// internally idempotent per-mutation, so calling them unconditionally
		// here layers in whatever's missing without re-submitting anything
		// already committed.
		seedClinicTemplate(ctx, conn, adminKey)
		fmt.Println("==> template clinic: " + clinicTplKey + " availableAt building, permits CreateAppointment/RescheduleAppointment/SetAppointmentStatus")
		seedWellnessTemplate(ctx, conn, adminKey)
		fmt.Println("==> template wellness: " + wellnessTplKey + " availableAt building, permits CreateBooking/CancelBooking")
		seedCafeTemplate(ctx, conn, adminKey)
		fmt.Println("==> template cafe: " + cafeTplKey + " availableAt building, permits OpenTab/Settle")
		sessKey := seedWellnessEntities(ctx, conn, adminKey)
		fmt.Println("==> studio+session: " + studioKey + " locatedAt building; " + sessKey + " bookable")
		seedClinicProvider(ctx, conn, adminKey)
		fmt.Println("==> provider: " + providerKey + " practicesAt building")
		seedLocationPresentation(ctx, conn, adminKey)
		frontOfHouseRoleKey := "vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")
		staffKey := ensureStaff(ctx, conn, adminKey, frontOfHouseRoleKey, consumerRoleKey, buildingKey, staffName, staffEmail)
		fmt.Printf("==> staff:           %s (%s) worksAt building, holds frontOfHouse\n", staffKey, staffName)
		seedStaffWorklistApplication(ctx, conn, adminKey, staffKey)
		fmt.Println("FACET_STAFF_NANOID=" + strings.TrimPrefix(staffKey, "vtx.identity."))
		maintKey := seedMaintenanceBeat(ctx, conn, adminKey, consumerRoleKey)
		fmt.Println("FACET_MAINT_NANOID=" + strings.TrimPrefix(maintKey, "vtx.identity."))

		oseiIdentityKey := seedOseiProvider(ctx, conn, adminKey, providerRoleKey)
		fmt.Printf("==> provider (bound): %s (%s) practicesAt building, identifiedBy %s\n", oseiProviderKey, oseiName, oseiIdentityKey)
		fmt.Println("FACET_PROVIDER_NANOID=" + strings.TrimPrefix(oseiIdentityKey, "vtx.identity."))

		if tenant1Key != "" {
			seedRileyClinicWorld(ctx, conn, adminKey, tenant1Key)
			fmt.Println("==> patient:         " + rileyPatientKey + " (" + tenant1Name + ") identifiedBy tenant1, booked with Osei + Patel")
		}

		if tenant2Key != "" {
			kaiIdentityKey := seedKaiServiceProvider(ctx, conn, adminKey, providerRoleKey, tenant2Key)
			fmt.Printf("==> serviceprovider (bound): %s (%s) providedBy laundry template, identifiedBy %s\n", kaiServiceProviderKey, kaiBusinessName, kaiIdentityKey)
			fmt.Println("FACET_LAUNDRY_NANOID=" + strings.TrimPrefix(kaiIdentityKey, "vtx.identity."))

			seedSamMultiHat(ctx, conn, adminKey, tenant2Key, frontOfHouseRoleKey, providerRoleKey)
			fmt.Printf("==> multi-hat:       %s (%s) gains frontOfHouse (worksAt building) + instructor (teachesAt studio, ledBy Evening Flow)\n", tenant2Key, tenant2Name)
		}
		return
	}

	// --- building + two units --------------------------------------------

	submitOp(ctx, conn, adminKey, "CreateLocation", "location",
		map[string]any{"locationType": "building", "locationId": buildingID,
			"presentation": showcaseLocationNames[buildingKey]}, nil)
	fmt.Println("==> building:        " + buildingKey)

	submitOp(ctx, conn, adminKey, "CreateLocation", "location",
		map[string]any{"locationType": "unit", "locationId": unit1ID,
			"presentation": showcaseLocationNames[unit1Key]}, nil)
	submitOp(ctx, conn, adminKey, "WireContainedIn", "location",
		map[string]any{"child": unit1Key, "parent": buildingKey},
		&processor.ContextHint{Reads: []string{unit1Key, buildingKey}})
	fmt.Println("==> unit1:           " + unit1Key + " containedIn building")

	submitOp(ctx, conn, adminKey, "CreateLocation", "location",
		map[string]any{"locationType": "unit", "locationId": unit2ID,
			"presentation": showcaseLocationNames[unit2Key]}, nil)
	submitOp(ctx, conn, adminKey, "WireContainedIn", "location",
		map[string]any{"child": unit2Key, "parent": buildingKey},
		&processor.ContextHint{Reads: []string{unit2Key, buildingKey}})
	fmt.Println("==> unit2:           " + unit2Key + " containedIn building")

	// --- two tenants, each residing in their own unit ---------------------

	tenant1Key := seedTenant(ctx, conn, adminKey, consumerRoleKey, unit1Key, tenant1Name, tenant1Email)
	fmt.Printf("==> tenant1:         %s (%s) residesIn unit1\n", tenant1Key, tenant1Name)
	tenant2Key := seedTenant(ctx, conn, adminKey, consumerRoleKey, unit2Key, tenant2Name, tenant2Email)
	fmt.Printf("==> tenant2:         %s (%s) residesIn unit2\n", tenant2Key, tenant2Name)

	// --- one staff persona, working at the building (not residing in a unit) --

	frontOfHouseRoleKey := "vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")
	staffKey := ensureStaff(ctx, conn, adminKey, frontOfHouseRoleKey, consumerRoleKey, buildingKey, staffName, staffEmail)
	fmt.Printf("==> staff:           %s (%s) worksAt building, holds frontOfHouse\n", staffKey, staffName)

	// --- two service templates, correct families, both availableAt the building --

	requestServiceMeta := findOpMetaByType(ctx, conn, "RequestService")

	seedTemplate(ctx, conn, adminKey, requestServiceMeta, laundryTplID, "laundry",
		map[string]any{"name": "Maple Laundry", "description": "Wash-and-fold, 24h turnaround", "icon": "laundry", "category": "home"})
	fmt.Println("==> template laundry: " + laundryTplKey + " availableAt building")

	seedTemplate(ctx, conn, adminKey, requestServiceMeta, fitnessTplID, "fitness",
		map[string]any{"name": "Riverside Fitness Studio", "description": "Drop-in classes + open gym", "icon": "fitness", "category": "wellness"})
	fmt.Println("==> template fitness: " + fitnessTplKey + " availableAt building")

	// --- clinic "book an appointment" template, mixed-use in the same building
	// (mirrors mixed-use-composition-design.md's front-desk clinic precedent;
	// catalog reachability needs the availableAt container on the actor's own
	// containedIn chain — a separate, unrelated clinic building would never
	// surface) — permits the three clinic self-scope ops directly rather than
	// RequestService, so they carry their own auth (edge-showcase-app-design.md
	// §7.4) --

	seedClinicTemplate(ctx, conn, adminKey)
	fmt.Println("==> template clinic: " + clinicTplKey + " availableAt building, permits CreateAppointment/RescheduleAppointment/SetAppointmentStatus")

	// --- wellness "book a class" template, same mixed-use building precedent
	// as clinic above — permits CreateBooking/CancelBooking directly (they
	// also carry their own scope=self auth, not RequestService's
	// authContext.service path) --

	seedWellnessTemplate(ctx, conn, adminKey)
	fmt.Println("==> template wellness: " + wellnessTplKey + " availableAt building, permits CreateBooking/CancelBooking")

	// --- café "house tab" template, same mixed-use building precedent as
	// clinic/wellness above — permits OpenTab/Settle directly (they also
	// carry their own scope=self auth, not RequestService's
	// authContext.service path) --

	seedCafeTemplate(ctx, conn, adminKey)
	fmt.Println("==> template cafe: " + cafeTplKey + " availableAt building, permits OpenTab/Settle")

	// --- browsable dispatch-target entities: a located studio with one
	// bookable session, and a provider practicing at the building (facet-
	// entity-browse-design.md §4 step 2 — without these the browse view is
	// correct and empty) --

	sessKey := seedWellnessEntities(ctx, conn, adminKey)
	fmt.Println("==> studio+session: " + studioKey + " locatedAt building; " + sessKey + " bookable")
	seedClinicProvider(ctx, conn, adminKey)
	fmt.Println("==> provider: " + providerKey + " practicesAt building")

	// --- one completed instance for tenant1 (the Activity timeline seed) --

	instReply := submitOp(ctx, conn, adminKey, "CreateServiceInstance", "service",
		map[string]any{"family": "laundry", "instanceId": instance1ID, "template": laundryTplKey, "providedTo": tenant1Key},
		&processor.ContextHint{Reads: []string{laundryTplKey, tenant1Key}})
	instKey := instReply.PrimaryKey
	submitOp(ctx, conn, adminKey, "RecordServiceOutcome", "service",
		map[string]any{"instanceKey": instKey, "status": "completed", "completedAt": "2026-07-15T09:00:00Z"},
		&processor.ContextHint{Reads: []string{instKey}})
	fmt.Println("==> instance:        " + instKey + " (laundry, tenant1, completed) — Activity timeline seed")

	seedStaffWorklistApplication(ctx, conn, adminKey, staffKey)

	// Cold-start race guard (verticals.md "Facet cold-start races the cap
	// projection", ef45e83): wait for both tenants' consumer role grant to
	// project before this loader returns, so an immediate `make up-facet`
	// (or a login attempt) never races cap.roles.<tenant>.
	waitForRoleGrant(ctx, conn, tenant1Key, "ctrl.refractor.register")
	waitForRoleGrant(ctx, conn, tenant2Key, "ctrl.refractor.register")
	// The staff persona's grants come from the vertical packages, not the
	// control-plane role — wait on one of them so a login never races
	// cap.roles.<staff>.
	waitForRoleGrant(ctx, conn, staffKey, "DecideLeaseApplication")

	retireLegacyTemplates(ctx, conn)

	maintKey := seedMaintenanceBeat(ctx, conn, adminKey, consumerRoleKey)

	oseiIdentityKey := seedOseiProvider(ctx, conn, adminKey, providerRoleKey)
	fmt.Printf("==> provider (bound): %s (%s) practicesAt building, identifiedBy %s\n", oseiProviderKey, oseiName, oseiIdentityKey)

	seedRileyClinicWorld(ctx, conn, adminKey, tenant1Key)
	fmt.Println("==> patient:         " + rileyPatientKey + " (" + tenant1Name + ") identifiedBy tenant1, booked with Osei + Patel")

	kaiIdentityKey := seedKaiServiceProvider(ctx, conn, adminKey, providerRoleKey, tenant2Key)
	fmt.Printf("==> serviceprovider (bound): %s (%s) providedBy laundry template, identifiedBy %s\n", kaiServiceProviderKey, kaiBusinessName, kaiIdentityKey)

	seedSamMultiHat(ctx, conn, adminKey, tenant2Key, frontOfHouseRoleKey, providerRoleKey)
	fmt.Printf("==> multi-hat:       %s (%s) gains frontOfHouse (worksAt building) + instructor (teachesAt studio, ledBy Evening Flow)\n", tenant2Key, tenant2Name)

	fmt.Println()
	fmt.Println("==> showcase world seeded.")
	fmt.Println("FACET_TENANT1_NANOID=" + strings.TrimPrefix(tenant1Key, "vtx.identity."))
	fmt.Println("FACET_TENANT2_NANOID=" + strings.TrimPrefix(tenant2Key, "vtx.identity."))
	fmt.Println("FACET_STAFF_NANOID=" + strings.TrimPrefix(staffKey, "vtx.identity."))
	fmt.Println("FACET_MAINT_NANOID=" + strings.TrimPrefix(maintKey, "vtx.identity."))
	fmt.Println("FACET_PROVIDER_NANOID=" + strings.TrimPrefix(oseiIdentityKey, "vtx.identity."))
	fmt.Println("FACET_LAUNDRY_NANOID=" + strings.TrimPrefix(kaiIdentityKey, "vtx.identity."))
}

// seedMaintenanceBeat loads the offline maintenance beat
// (facet-staff-worlds-design.md §6 F5): a back-of-house tech who worksAt the
// building, and a work order at Unit 1 queued to their role — claimable from
// the tech's own Facet mirror, resolvable under the task's ephemeral grant.
//
// The tech is a SECOND staff persona and is deliberately not the front-desk
// one: the whole F5 argument is that a maintenance world is nameless (D3 —
// unit/equipment-scoped work carries no resident PII, which is what lets it
// ride the SYNC plane at all), while the front desk's worklist is
// PII-bearing and server-paned. One binary, two staff worlds, different
// shapes — that only shows if two personas exist.
//
// The work order + its task roll their ids by UTC day, the wellness session's
// own reason: a reseed against a world the nightly wipe did NOT clear must
// still offer an UNRESOLVED work order, or the demo's maintenance tab is
// permanently a list of finished work. Two reseeds on the same day converge on
// the same ids (per-mutation idempotent, like every seeder here).
//
// Returns the tech's identity key.
func seedMaintenanceBeat(ctx context.Context, conn *substrate.Conn, adminKey, consumerRoleKey string) string {
	backOfHouseRoleKey := "vtx.role." + pkgmgr.RoleID("identity-domain", "backOfHouse")
	techKey := ensureMaintenanceTech(ctx, conn, adminKey, backOfHouseRoleKey, consumerRoleKey)
	fmt.Printf("==> maintenance:     %s (%s) worksAt building, holds backOfHouse\n", techKey, maintName)

	day := time.Now().UTC().Format("2006-01-02")
	woID := substrate.DeriveNanoID("showcase-workorder", day)
	woKey := "vtx.workorder." + woID
	if !alive(ctx, conn, woKey) {
		submitOp(ctx, conn, adminKey, "ReportIssue", "workOrder",
			map[string]any{"workOrderId": woID, "location": unit1Key, "priority": "urgent",
				"summary": "Basement riser valve is weeping — no phone signal down there"},
			&processor.ContextHint{Reads: []string{unit1Key}})
	}

	taskID := substrate.DeriveNanoID("showcase-workorder-task", day)
	taskKey := "vtx.task." + taskID
	if !alive(ctx, conn, taskKey) {
		resolveMeta := findOpMetaByType(ctx, conn, "ResolveWorkOrder")
		submitOp(ctx, conn, adminKey, "CreateTask", "task",
			map[string]any{"taskId": taskID, "queue": backOfHouseRoleKey,
				"forOperation": resolveMeta, "scopedTo": woKey,
				"expiresAt": time.Now().UTC().AddDate(0, 0, 30).Format(time.RFC3339)},
			&processor.ContextHint{
				Reads: []string{backOfHouseRoleKey, resolveMeta, woKey},
				// CreateTask's own absence-tolerant reads: the dedup key and
				// the assignee availability aspect, neither of which exists on
				// a queue-only task (Contract #2 §2.5 class (d)).
				OptionalReads: []string{taskKey},
			})
	}
	fmt.Println("==> work order:      " + woKey + " at Unit 1, queued to backOfHouse via " + taskKey)
	return techKey
}

// ensureMaintenanceTech resolves the showcase maintenance persona, minting it
// only if absent. It looks the persona up by its holdsRole link rather than by
// worksAt (ensureStaff's probe): TWO personas now work at the building, so a
// worksAt scan can no longer tell them apart, while the role is exactly what
// distinguishes them. A tombstoned worksAt link is re-wired rather than read as
// absent — the same revive path ensureStaff needed once a retraction vector had
// unwired its persona.
//
// consumerRoleKey excludes any candidate that ALSO holds consumer (§3.4's Sam
// Okafor, once he gains frontOfHouse/backOfHouse alongside his existing
// consumer hat, must never be resolved as the canonical staff-only persona —
// Theo, like Dana, is deliberately staff-only; see ensureStaff).
func ensureMaintenanceTech(ctx context.Context, conn *substrate.Conn, adminKey, roleKey, consumerRoleKey string) string {
	js := conn.JetStream()
	coreKV, err := js.KeyValue(ctx, bootstrap.CoreKVBucket)
	must(err, "open core-kv")
	allKeys, err := pkgverify.ListAllKeys(ctx, coreKV)
	must(err, "list core-kv keys")

	existing, _ := findLinkedIdentity(ctx, coreKV, allKeys, "holdsRole", roleKey, consumerRoleKey)
	if existing == "" {
		return seedStaff(ctx, conn, adminKey, roleKey, buildingKey, maintName, maintEmail)
	}
	if !alive(ctx, conn, linkKey(existing, "worksAt", buildingKey)) {
		submitOp(ctx, conn, adminKey, "WireWorksAt", "serviceLocation",
			map[string]any{"identity": existing, "location": buildingKey},
			wireHint(existing, "worksAt", buildingKey))
		fmt.Printf("==> healed:          re-wired %s worksAt building (link was absent or tombstoned)\n", existing)
	}
	return existing
}

// seedTenant mints an unclaimed identity, grants consumer, wires residesIn,
// and (mirroring seed-edge-demo.go's §3.0 note) flips .state to claimed
// directly via UpdateIdentityState rather than the real ClaimIdentity
// ceremony — that op's own mutation unconditionally re-creates the
// holdsRole→consumer link, which would collide with the AssignRole grant
// above (RevisionConflict). Returns the minted identity key.
func seedTenant(ctx context.Context, conn *substrate.Conn, adminKey, consumerRoleKey, unitKey, name, email string) string {
	salt, err := substrate.NewNanoID()
	must(err, "generate tenant claim-key salt")
	claimKeyPlaintext := "showcase-" + salt
	claimSum := mustSHA256Hex(claimKeyPlaintext)
	reply := submitOp(ctx, conn, adminKey, "CreateUnclaimedIdentity", "identity",
		map[string]any{"name": name, "email": email, "claimKeyHash": claimSum}, nil)
	tenantKey := reply.PrimaryKey

	submitOp(ctx, conn, adminKey, "AssignRole", "",
		map[string]any{"actorKey": tenantKey, "roleKey": consumerRoleKey},
		&processor.ContextHint{Reads: []string{tenantKey, consumerRoleKey}})
	submitOp(ctx, conn, adminKey, "WireResidesIn", "serviceLocation",
		map[string]any{"identity": tenantKey, "location": unitKey},
		wireHint(tenantKey, "residesIn", unitKey))
	submitOp(ctx, conn, adminKey, "UpdateIdentityState", "identity",
		map[string]any{"identityKey": tenantKey, "newState": "claimed"},
		&processor.ContextHint{Reads: []string{tenantKey, tenantKey + ".state"}})
	return tenantKey
}

// ensureStaff returns the showcase staff persona, minting it only if the world
// does not already have one. The building's liveness gates the whole
// already-seeded branch, but a staff persona is a LATER increment — an existing
// showcase world predates it — so this layers in like the other increments.
//
// Unlike the template seeds it cannot be idempotent per-mutation:
// CreateUnclaimedIdentity mints a fresh NanoID on every call, so a blind re-run
// would quietly accumulate a second, third, fourth staff identity all wired to
// the same building. The worksAt link IS the identity of the persona here, so
// recovering by that link is what makes the rerun safe.
//
// A tombstoned (or never-created) worksAt link is recovered and (re-)wired,
// not treated as an absent persona: the identity behind it still holds the
// fixed staff email, so minting a replacement collides on the identity index
// and fails the seed. That is the wedge an unwire (or a seed death between
// AssignRole and WireWorksAt) left behind — the persona existed, was
// unreachable, and could not be re-minted either.
//
// Looked up by its holdsRole link, not by a worksAt scan of the building:
// once a second persona (the maintenance tech, F5) also worksAt the same
// building, a worksAt-only scan can no longer tell the two apart and — since
// findLinkedIdentity returns the alphabetically-first live candidate — can
// silently resolve to the WRONG identity. Mirrors ensureMaintenanceTech's own
// role-scoped lookup, added when that ambiguity first appeared.
//
// consumerRoleKey excludes any candidate that ALSO holds consumer: the seed
// invariant is that Dana is deliberately staff-only (seedStaff's own doc
// comment above), so once a SECOND frontOfHouse holder exists who is ALSO a
// consumer (§3.4's Sam Okafor, granted frontOfHouse on top of his existing
// resident hat), a bare holdsRole(frontOfHouse) scan can resolve to either —
// the 35ca90f5 mis-resolution one level up from the worksAt-only bug above.
// Filtering the consumer-holding candidate out of contention is what keeps
// FACET_STAFF_NANOID pinned to the pure-staff persona regardless of how many
// other identities pick up the same role.
func ensureStaff(ctx context.Context, conn *substrate.Conn, adminKey, roleKey, consumerRoleKey, buildingKey, name, email string) string {
	js := conn.JetStream()
	coreKV, err := js.KeyValue(ctx, bootstrap.CoreKVBucket)
	must(err, "open core-kv")
	allKeys, err := pkgverify.ListAllKeys(ctx, coreKV)
	must(err, "list core-kv keys")

	existing, _ := findLinkedIdentity(ctx, coreKV, allKeys, "holdsRole", roleKey, consumerRoleKey)
	if existing == "" {
		return seedStaff(ctx, conn, adminKey, roleKey, buildingKey, name, email)
	}
	if !alive(ctx, conn, linkKey(existing, "worksAt", buildingKey)) {
		submitOp(ctx, conn, adminKey, "WireWorksAt", "serviceLocation",
			map[string]any{"identity": existing, "location": buildingKey},
			wireHint(existing, "worksAt", buildingKey))
		fmt.Printf("==> healed:          re-wired %s worksAt building (link was absent or tombstoned)\n", existing)
	}
	return existing
}

// seedStaff mints the showcase staff persona: an identity holding
// `frontOfHouse` and wired worksAt the BUILDING (not residesIn a unit — staff
// work here, they do not live here). It is the resident seed's mirror image,
// and the difference is the whole point of the staff spine: a resident's world
// composes from residesIn, a staff member's from worksAt + holdsRole.
//
// The persona deliberately gets NO residesIn link and NO consumer role, so its
// world is purely staff-derived. It also holds no `operator` role: everything
// it can do comes from the frontOfHouse grants the vertical packages declare
// (DecideLeaseApplication, the two clinic schedule ops, the three café tab ops,
// CreateSession) — which is what makes it the first showcase actor whose write
// surface is narrower than root.
//
// State is flipped via UpdateIdentityState for the same reason seedTenant does
// it: the real ClaimIdentity ceremony re-creates a holdsRole link that would
// collide with the AssignRole grant above.
func seedStaff(ctx context.Context, conn *substrate.Conn, adminKey, roleKey, buildingKey, name, email string) string {
	salt, err := substrate.NewNanoID()
	must(err, "generate staff claim-key salt")
	claimSum := mustSHA256Hex("showcase-staff-" + salt)
	reply := submitOp(ctx, conn, adminKey, "CreateUnclaimedIdentity", "identity",
		map[string]any{"name": name, "email": email, "claimKeyHash": claimSum}, nil)
	staffKey := reply.PrimaryKey

	submitOp(ctx, conn, adminKey, "AssignRole", "",
		map[string]any{"actorKey": staffKey, "roleKey": roleKey},
		&processor.ContextHint{Reads: []string{staffKey, roleKey}})
	submitOp(ctx, conn, adminKey, "WireWorksAt", "serviceLocation",
		map[string]any{"identity": staffKey, "location": buildingKey},
		wireHint(staffKey, "worksAt", buildingKey))
	submitOp(ctx, conn, adminKey, "UpdateIdentityState", "identity",
		map[string]any{"identityKey": staffKey, "newState": "claimed"},
		&processor.ContextHint{Reads: []string{staffKey, staffKey + ".state"}})
	return staffKey
}

// seedTemplate mints a service template with the given fixed id + family +
// presentation, wires it availableAt the showcase building, and permits
// RequestService.
func seedTemplate(ctx context.Context, conn *substrate.Conn, adminKey, requestServiceMeta, templateID, family string, presentation map[string]any) {
	tplKey := "vtx.service." + templateID
	submitOp(ctx, conn, adminKey, "CreateServiceTemplate", "service",
		map[string]any{"family": family, "templateId": templateID, "presentation": presentation}, nil)
	submitOp(ctx, conn, adminKey, "WireAvailableAt", "serviceLocation",
		map[string]any{"service": tplKey, "location": buildingKey},
		wireHint(tplKey, "availableAt", buildingKey))
	submitOp(ctx, conn, adminKey, "WirePermitsOperation", "serviceLocation",
		map[string]any{"service": tplKey, "operation": requestServiceMeta},
		wireHint(tplKey, "permitsOperation", requestServiceMeta))
}

// seedClinicTemplate mints the clinic "book an appointment" service
// template, wires it availableAt the showcase building, and
// permitsOperation-links it directly to clinic-domain's three self-scope
// consumer ops (CreateAppointment, RescheduleAppointment,
// SetAppointmentStatus) — the catalog-path wiring named in
// edge-showcase-app-design.md §7.4's residual note. Unlike laundry/fitness,
// these ops don't dispatch through service-domain's authContext.service
// (they carry their own scope=self grants), so the template links straight
// to each op-meta rather than to RequestService.
//
// Each of the four mutations is individually idempotency-checked (not just
// the template's own existence) so a rerun after a partial failure — e.g.
// the template + availableAt link landed but an op-meta lookup then failed
// — resumes exactly where it left off rather than either erroring on a
// duplicate create or silently skipping the still-missing links.
func seedClinicTemplate(ctx context.Context, conn *substrate.Conn, adminKey string) {
	if !alive(ctx, conn, clinicTplKey) {
		submitOp(ctx, conn, adminKey, "CreateServiceTemplate", "service",
			map[string]any{"family": "clinic", "templateId": clinicTplID, "presentation": map[string]any{
				"name":        "Riverside Clinic",
				"description": "Book, reschedule, or cancel a clinic appointment",
				"icon":        "clinic",
				"category":    "health",
			}}, nil)
	}

	availableAtLnk := linkKey(clinicTplKey, "availableAt", buildingKey)
	if !alive(ctx, conn, availableAtLnk) {
		submitOp(ctx, conn, adminKey, "WireAvailableAt", "serviceLocation",
			map[string]any{"service": clinicTplKey, "location": buildingKey},
			wireHint(clinicTplKey, "availableAt", buildingKey))
	}

	for _, opType := range []string{"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus"} {
		opMeta := findOpMetaByType(ctx, conn, opType)
		permitsLnk := linkKey(clinicTplKey, "permitsOperation", opMeta)
		if alive(ctx, conn, permitsLnk) {
			continue
		}
		submitOp(ctx, conn, adminKey, "WirePermitsOperation", "serviceLocation",
			map[string]any{"service": clinicTplKey, "operation": opMeta},
			wireHint(clinicTplKey, "permitsOperation", opMeta))
	}
}

// seedWellnessTemplate mints the wellness "book a class" service template,
// wires it availableAt the showcase building, and permitsOperation-links it
// directly to wellness-domain's two self-scope consumer ops (CreateBooking,
// CancelBooking) — the catalog-path wiring named in
// edge-showcase-app-design.md §7's residual note, mirroring
// seedClinicTemplate exactly (same building, same permitsOperation pattern;
// wellness's own studio/session vertices are unrelated to catalog
// reachability, which needs only the availableAt container on the actor's
// own containedIn chain).
func seedWellnessTemplate(ctx context.Context, conn *substrate.Conn, adminKey string) {
	if !alive(ctx, conn, wellnessTplKey) {
		submitOp(ctx, conn, adminKey, "CreateServiceTemplate", "service",
			map[string]any{"family": "wellness", "templateId": wellnessTplID, "presentation": map[string]any{
				"name":        "Riverside Wellness Studio",
				"description": "Book, or cancel, a class",
				"icon":        "wellness",
				"category":    "wellness",
			}}, nil)
	}

	availableAtLnk := linkKey(wellnessTplKey, "availableAt", buildingKey)
	if !alive(ctx, conn, availableAtLnk) {
		submitOp(ctx, conn, adminKey, "WireAvailableAt", "serviceLocation",
			map[string]any{"service": wellnessTplKey, "location": buildingKey},
			wireHint(wellnessTplKey, "availableAt", buildingKey))
	}

	for _, opType := range []string{"CreateBooking", "CancelBooking"} {
		opMeta := findOpMetaByType(ctx, conn, opType)
		permitsLnk := linkKey(wellnessTplKey, "permitsOperation", opMeta)
		if alive(ctx, conn, permitsLnk) {
			continue
		}
		submitOp(ctx, conn, adminKey, "WirePermitsOperation", "serviceLocation",
			map[string]any{"service": wellnessTplKey, "operation": opMeta},
			wireHint(wellnessTplKey, "permitsOperation", opMeta))
	}
}

// seedCafeTemplate mints the café "house tab" service template, wires it
// availableAt the showcase building, and permitsOperation-links it directly
// to cafe-domain's two self-scope consumer ops (OpenTab, Settle) — the
// catalog-path wiring named in edge-showcase-app-design.md §7.6's residual
// note, mirroring seedClinicTemplate/seedWellnessTemplate exactly (same
// building, same permitsOperation pattern; a resident opening a tab needs no
// prior "browsing" entity the way a clinic provider or wellness session is,
// so there is nothing else for this template to link besides the two ops
// themselves).
func seedCafeTemplate(ctx context.Context, conn *substrate.Conn, adminKey string) {
	if !alive(ctx, conn, cafeTplKey) {
		submitOp(ctx, conn, adminKey, "CreateServiceTemplate", "service",
			map[string]any{"family": "cafe", "templateId": cafeTplID, "presentation": map[string]any{
				"name":        "Riverside Café",
				"description": "Open, or settle, a house tab",
				"icon":        "cafe",
				"category":    "home",
			}}, nil)
	}

	availableAtLnk := linkKey(cafeTplKey, "availableAt", buildingKey)
	if !alive(ctx, conn, availableAtLnk) {
		submitOp(ctx, conn, adminKey, "WireAvailableAt", "serviceLocation",
			map[string]any{"service": cafeTplKey, "location": buildingKey},
			wireHint(cafeTplKey, "availableAt", buildingKey))
	}

	for _, opType := range []string{"OpenTab", "Settle"} {
		opMeta := findOpMetaByType(ctx, conn, opType)
		permitsLnk := linkKey(cafeTplKey, "permitsOperation", opMeta)
		if alive(ctx, conn, permitsLnk) {
			continue
		}
		submitOp(ctx, conn, adminKey, "WirePermitsOperation", "serviceLocation",
			map[string]any{"service": cafeTplKey, "operation": opMeta},
			wireHint(cafeTplKey, "permitsOperation", opMeta))
	}
}

// seedWellnessEntities mints the showcase wellness studio — locatedAt the
// showcase building, the authZ-free browse-reachability link facet-entity-
// browse-design.md §3 F1 adds — and one bookable class session at it. The
// studio keeps its fixed checked-in handle; the SESSION id rolls by UTC day
// (a deterministic derivation over the day) so a reseed against a world the
// nightly wipe did NOT clear — a redeploy, or a box whose reset timer lapsed —
// still mints a class that starts in the FUTURE, instead of leaving the last
// deploy's now-past session as the only thing Nearby can offer (a demo visitor
// then can't book). Two reseeds on the same UTC day converge on the same id
// (per-mutation idempotent, mirroring seedClinicTemplate). Past sessions from
// prior days linger in Core KV but the Nearby renderer hides them by startsAt;
// the nightly wipe clears them for good. Returns the live session key.
func seedWellnessEntities(ctx context.Context, conn *substrate.Conn, adminKey string) string {
	if !alive(ctx, conn, studioKey) {
		submitOp(ctx, conn, adminKey, "CreateStudio", "studio",
			map[string]any{"name": "Riverside Movement Studio", "studioId": studioID, "location": buildingKey},
			&processor.ContextHint{Reads: []string{buildingKey}})
	}
	start := time.Now().UTC().Add(24 * time.Hour).Truncate(15 * time.Minute)
	end := start.Add(time.Hour)
	sessID := substrate.DeriveNanoID("showcase-wellness-session", start.Format("2006-01-02"))
	sessKey := "vtx.session." + sessID
	if !alive(ctx, conn, sessKey) {
		submitOp(ctx, conn, adminKey, "CreateSession", "session",
			map[string]any{"studio": studioKey, "sessionId": sessID, "name": "Vinyasa Flow",
				"startsAt": start.Format(time.RFC3339), "endsAt": end.Format(time.RFC3339), "capacity": 12},
			&processor.ContextHint{
				Reads: []string{studioKey},
				// The studio's per-cell slot claims: absent until something
				// books the cell, so optional (Contract #2 §2.5).
				OptionalReads: slotClaimKeys(studioKey, start, end),
			})
	}
	return sessKey
}

// seedClinicProvider mints the showcase clinic provider and assigns it to
// practice at the showcase building — the practicesAt link the provider
// browse walk (edgeEntityProviders) resolves through. Fixed handle;
// per-mutation idempotent like the template seeders.
func seedClinicProvider(ctx context.Context, conn *substrate.Conn, adminKey string) {
	if !alive(ctx, conn, providerKey) {
		submitOp(ctx, conn, adminKey, "CreateProvider", "provider",
			map[string]any{"fullName": "Dr. Maya Patel", "specialty": "Family Medicine", "providerId": providerID}, nil)
	}
	practicesLnk := "lnk.provider." + providerID + ".practicesAt." + strings.TrimPrefix(buildingKey, "vtx.")
	if !alive(ctx, conn, practicesLnk) {
		submitOp(ctx, conn, adminKey, "AssignProviderSite", "clinicSiteAssignment",
			map[string]any{"provider": providerKey, "building": buildingKey},
			&processor.ContextHint{
				Reads: []string{providerKey, buildingKey},
				// The per-pair link is read on demand by the script (create /
				// revive / no-op), so it is declared optional, mirroring
				// clinic-domain's own site_integration_test submit.
				OptionalReads: []string{practicesLnk},
			})
	}
}

// seedOseiProvider mints the showcase's SECOND clinic provider — Dr. Amara
// Osei, BOUND to a login identity (persona-worlds-design.md Fire W0's
// scoping positive; Dr. Maya Patel, providerKey above, stays UNBOUND as the
// negative — a provider-scoped feed must show Osei's own appointments and
// must never show Patel's). Fixed handle; per-mutation idempotent, mirroring
// seedClinicProvider exactly. Returns Osei's identity key.
func seedOseiProvider(ctx context.Context, conn *substrate.Conn, adminKey, providerRoleKey string) string {
	if !alive(ctx, conn, oseiProviderKey) {
		submitOp(ctx, conn, adminKey, "CreateProvider", "provider",
			map[string]any{"fullName": oseiName, "specialty": "Sports Medicine", "providerId": oseiProviderID}, nil)
	}
	practicesLnk := linkKey(oseiProviderKey, "practicesAt", buildingKey)
	if !alive(ctx, conn, practicesLnk) {
		submitOp(ctx, conn, adminKey, "AssignProviderSite", "clinicSiteAssignment",
			map[string]any{"provider": oseiProviderKey, "building": buildingKey},
			&processor.ContextHint{
				Reads:         []string{oseiProviderKey, buildingKey},
				OptionalReads: []string{practicesLnk},
			})
	}
	identityKey := ensureProviderIdentity(ctx, conn, adminKey, providerRoleKey, oseiProviderKey, oseiName, oseiEmail)
	waitForRoleGrant(ctx, conn, identityKey, "ctrl.refractor.register")
	return identityKey
}

// ensureProviderIdentity resolves the identity BindProviderIdentity has
// bound to the given FIXED clinic provider entity (entityKey), minting a
// fresh unclaimed identity and binding it only if the provider carries no
// bind yet. Unlike ensureStaff/ensureMaintenanceTech (whose PERSONA is
// unknown and recovered by scanning FOR an identity), the provider entity's
// own id is the fixed, checked-in half here — only the identity id is ever
// minted — so this is the recovery seam BindProviderIdentity's CreateOnly
// guard leaves: a second CreateUnclaimedIdentity + BindProviderIdentity
// attempt against an already-bound provider is rejected (ProviderAlreadyBound),
// so a rerun must find and return the existing bind rather than attempt a
// new one.
func ensureProviderIdentity(ctx context.Context, conn *substrate.Conn, adminKey, providerRoleKey, entityKey, name, email string) string {
	js := conn.JetStream()
	coreKV, err := js.KeyValue(ctx, bootstrap.CoreKVBucket)
	must(err, "open core-kv")
	allKeys, err := pkgverify.ListAllKeys(ctx, coreKV)
	must(err, "list core-kv keys")

	if existing, found := findBoundIdentity(allKeys, entityKey); found {
		return existing
	}

	salt, err := substrate.NewNanoID()
	must(err, "generate provider claim-key salt")
	claimSum := mustSHA256Hex("showcase-provider-" + salt)
	reply := submitOp(ctx, conn, adminKey, "CreateUnclaimedIdentity", "identity",
		map[string]any{"name": name, "email": email, "claimKeyHash": claimSum}, nil)
	identityKey := reply.PrimaryKey

	submitOp(ctx, conn, adminKey, "BindProviderIdentity", "provider",
		map[string]any{"providerKey": entityKey, "identityKey": identityKey},
		&processor.ContextHint{
			Reads: []string{entityKey, identityKey},
			OptionalReads: []string{
				entityKey + ".identityClaim",
				identityKey + ".providerClaim",
				linkKey(identityKey, "holdsRole", providerRoleKey),
			},
		})

	submitOp(ctx, conn, adminKey, "UpdateIdentityState", "identity",
		map[string]any{"identityKey": identityKey, "newState": "claimed"},
		&processor.ContextHint{Reads: []string{identityKey, identityKey + ".state"}})

	return identityKey
}

// seedKaiServiceProvider mints the showcase's laundry serviceprovider —
// Kai's Laundry Co., BOUND to a login identity and wired providedBy onto the
// existing laundry template (persona-worlds-design.md Fire W0's
// service-domain provider archetype). One OPEN instance providedTo Sam
// (tenant2) demonstrates the provider's own work queue; the existing
// completed instance (instance1ID) stays untouched — it is never given an
// outcome here. Fixed handles; per-mutation idempotent. Returns Kai's
// identity key.
func seedKaiServiceProvider(ctx context.Context, conn *substrate.Conn, adminKey, providerRoleKey, tenant2Key string) string {
	if !alive(ctx, conn, kaiServiceProviderKey) {
		submitOp(ctx, conn, adminKey, "CreateServiceProvider", "serviceprovider",
			map[string]any{"displayName": kaiBusinessName, "serviceProviderId": kaiServiceProviderID}, nil)
	}
	identityKey := ensureServiceProviderIdentity(ctx, conn, adminKey, providerRoleKey, kaiServiceProviderKey, kaiName, kaiEmail)
	waitForRoleGrant(ctx, conn, identityKey, "ctrl.refractor.register")

	providedByLnk := linkKey(laundryTplKey, "providedBy", kaiServiceProviderKey)
	if !alive(ctx, conn, providedByLnk) {
		submitOp(ctx, conn, adminKey, "WireProvidedBy", "service",
			map[string]any{"template": laundryTplKey, "providedBy": kaiServiceProviderKey},
			wireHint(laundryTplKey, "providedBy", kaiServiceProviderKey))
	}

	if !alive(ctx, conn, kaiInstanceKey) {
		submitOp(ctx, conn, adminKey, "CreateServiceInstance", "service",
			map[string]any{"family": "laundry", "instanceId": kaiInstanceID, "template": laundryTplKey, "providedTo": tenant2Key},
			&processor.ContextHint{Reads: []string{laundryTplKey, tenant2Key}})
	}
	return identityKey
}

// ensureServiceProviderIdentity is ensureProviderIdentity's service-domain
// mirror: resolves the identity BindServiceProviderIdentity has bound to the
// given FIXED serviceprovider entity, minting + binding a fresh one only if
// still unbound. See ensureProviderIdentity for the full rationale.
func ensureServiceProviderIdentity(ctx context.Context, conn *substrate.Conn, adminKey, providerRoleKey, entityKey, name, email string) string {
	js := conn.JetStream()
	coreKV, err := js.KeyValue(ctx, bootstrap.CoreKVBucket)
	must(err, "open core-kv")
	allKeys, err := pkgverify.ListAllKeys(ctx, coreKV)
	must(err, "list core-kv keys")

	if existing, found := findBoundIdentity(allKeys, entityKey); found {
		return existing
	}

	salt, err := substrate.NewNanoID()
	must(err, "generate service provider claim-key salt")
	claimSum := mustSHA256Hex("showcase-serviceprovider-" + salt)
	reply := submitOp(ctx, conn, adminKey, "CreateUnclaimedIdentity", "identity",
		map[string]any{"name": name, "email": email, "claimKeyHash": claimSum}, nil)
	identityKey := reply.PrimaryKey

	submitOp(ctx, conn, adminKey, "BindServiceProviderIdentity", "serviceprovider",
		map[string]any{"serviceProviderKey": entityKey, "identityKey": identityKey},
		&processor.ContextHint{
			Reads: []string{entityKey, identityKey},
			OptionalReads: []string{
				entityKey + ".identityClaim",
				identityKey + ".serviceProviderClaim",
				linkKey(identityKey, "holdsRole", providerRoleKey),
			},
		})

	submitOp(ctx, conn, adminKey, "UpdateIdentityState", "identity",
		map[string]any{"identityKey": identityKey, "newState": "claimed"},
		&processor.ContextHint{Reads: []string{identityKey, identityKey + ".state"}})

	return identityKey
}

// seedRileyClinicWorld gives Riley Chen (tenant1) a clinic patient record
// bound to their own login identity, and books one future appointment with
// EACH clinic provider — the Osei/Patel scoping positive/negative
// (persona-worlds-design.md Fire W0): Osei's own provider-scoped feed must
// show the Osei appointment and must NOT show the Patel one. Fixed patient
// handle; day-derived appointment ids on the 15-minute grid, offset a whole
// distinct days AND distinct fixed hours (futureDayAt) so neither the derived
// id nor the patient's own slot claims can collide between the two bookings —
// nor across a reseed a day later, when the +1-day booking lands on the +2-day
// booking's date (distinct hours keep them off the same patient-hub slot). A
// rerun on the same UTC day converges on the same ids (mirrors
// seedWellnessEntities/seedMaintenanceBeat's day-derived idiom).
func seedRileyClinicWorld(ctx context.Context, conn *substrate.Conn, adminKey, tenant1Key string) {
	if !alive(ctx, conn, rileyPatientKey) {
		submitOp(ctx, conn, adminKey, "CreatePatient", "patient",
			map[string]any{"fullName": tenant1Name, "patientId": rileyPatientID, "identityKey": tenant1Key},
			&processor.ContextHint{
				Reads:         []string{tenant1Key},
				OptionalReads: []string{tenant1Key + ".patientClaim"},
			})
	}

	oseiStart := futureDayAt(1, 14)
	oseiEnd := oseiStart.Add(30 * time.Minute)
	oseiApptID := substrate.DeriveNanoID("showcase-appointment-osei", oseiStart.Format("2006-01-02"))
	oseiApptKey := "vtx.appointment." + oseiApptID
	if !alive(ctx, conn, oseiApptKey) {
		submitOp(ctx, conn, adminKey, "CreateAppointment", "appointment",
			map[string]any{
				"patient": rileyPatientKey, "provider": oseiProviderKey, "appointmentId": oseiApptID,
				"startsAt": oseiStart.Format(time.RFC3339), "endsAt": oseiEnd.Format(time.RFC3339),
				"reason": "Sports physical",
			},
			&processor.ContextHint{
				Reads: []string{rileyPatientKey, oseiProviderKey},
				OptionalReads: append(
					slotClaimKeys(oseiProviderKey, oseiStart, oseiEnd),
					slotClaimKeys(rileyPatientKey, oseiStart, oseiEnd)...),
			})
	}

	patelStart := futureDayAt(2, 16)
	patelEnd := patelStart.Add(30 * time.Minute)
	patelApptID := substrate.DeriveNanoID("showcase-appointment-patel", patelStart.Format("2006-01-02"))
	patelApptKey := "vtx.appointment." + patelApptID
	if !alive(ctx, conn, patelApptKey) {
		submitOp(ctx, conn, adminKey, "CreateAppointment", "appointment",
			map[string]any{
				"patient": rileyPatientKey, "provider": providerKey, "appointmentId": patelApptID,
				"startsAt": patelStart.Format(time.RFC3339), "endsAt": patelEnd.Format(time.RFC3339),
				"reason": "Annual checkup",
			},
			&processor.ContextHint{
				Reads: []string{rileyPatientKey, providerKey},
				OptionalReads: append(
					slotClaimKeys(providerKey, patelStart, patelEnd),
					slotClaimKeys(rileyPatientKey, patelStart, patelEnd)...),
			})
	}
}

// seedSamMultiHat layers the §3.4 "one human, many hats" acceptance scenario
// onto Sam Okafor (tenant2): consumer + residesIn (already seeded) stays
// untouched; this adds frontOfHouse + worksAt (the front-desk hat) and the
// wellness instructor archetype (provider role via BindInstructorIdentity,
// teachesAt the showcase studio, ledBy on a second, own-led session) — one
// identity, one login, three bindings. Sam's SECOND session lives in its own
// day-derived id namespace, two whole days out (mirroring the appointment
// offsets above, for the same collision-free reason), so it is always minted
// fresh on the first run of this increment: the existing day-rolled Vinyasa
// Flow session was created with no instructor and cannot be re-wired after
// the fact. Per-mutation idempotent; safe to call on every run once
// ensureStaff/ensureMaintenanceTech's hardening (above) is in place, since
// Sam then also holds frontOfHouse — the very ambiguity that hardening
// exists to resolve.
func seedSamMultiHat(ctx context.Context, conn *substrate.Conn, adminKey, tenant2Key, frontOfHouseRoleKey, providerRoleKey string) {
	frontOfHouseLnk := linkKey(tenant2Key, "holdsRole", frontOfHouseRoleKey)
	if !alive(ctx, conn, frontOfHouseLnk) {
		submitOp(ctx, conn, adminKey, "AssignRole", "",
			map[string]any{"actorKey": tenant2Key, "roleKey": frontOfHouseRoleKey},
			&processor.ContextHint{
				Reads:         []string{tenant2Key, frontOfHouseRoleKey},
				OptionalReads: []string{frontOfHouseLnk},
			})
	}
	if !alive(ctx, conn, linkKey(tenant2Key, "worksAt", buildingKey)) {
		submitOp(ctx, conn, adminKey, "WireWorksAt", "serviceLocation",
			map[string]any{"identity": tenant2Key, "location": buildingKey},
			wireHint(tenant2Key, "worksAt", buildingKey))
	}

	if !alive(ctx, conn, samInstructorKey) {
		submitOp(ctx, conn, adminKey, "CreateInstructor", "instructor",
			map[string]any{"displayName": tenant2Name, "studio": studioKey, "instructorId": samInstructorID},
			&processor.ContextHint{Reads: []string{studioKey}})
	}

	identifiedByLnk := linkKey(samInstructorKey, "identifiedBy", tenant2Key)
	if !alive(ctx, conn, identifiedByLnk) {
		submitOp(ctx, conn, adminKey, "BindInstructorIdentity", "instructor",
			map[string]any{"instructorKey": samInstructorKey, "identityKey": tenant2Key},
			&processor.ContextHint{
				Reads: []string{samInstructorKey, tenant2Key},
				OptionalReads: []string{
					samInstructorKey + ".identityClaim",
					tenant2Key + ".instructorClaim",
					linkKey(tenant2Key, "holdsRole", providerRoleKey),
				},
			})
	}

	waitForRoleGrant(ctx, conn, tenant2Key, "ctrl.refractor.register")

	start := futureDayAt(2, 19)
	end := start.Add(time.Hour)
	sessID := substrate.DeriveNanoID("showcase-wellness-session-sam", start.Format("2006-01-02"))
	sessKey := "vtx.session." + sessID
	if !alive(ctx, conn, sessKey) {
		submitOp(ctx, conn, adminKey, "CreateSession", "session",
			map[string]any{
				"studio": studioKey, "sessionId": sessID, "name": "Evening Flow with Sam",
				"startsAt": start.Format(time.RFC3339), "endsAt": end.Format(time.RFC3339),
				"capacity": 12, "instructor": samInstructorKey,
			},
			&processor.ContextHint{
				Reads:         []string{studioKey, samInstructorKey},
				OptionalReads: slotClaimKeys(studioKey, start, end),
			})
	}
}

// futureDayAt returns the instant `days` days ahead at a fixed whole hour in
// UTC. The wall-clock slot is a function of the entity, NOT of when the seed
// runs — so two day-derived entities at different day offsets can never
// rendezvous on the same calendar day at the same slot (which would collide on
// a shared patient/studio hub when a reseed one day later lands the +1-day
// entity on the +2-day entity's date). The date is stable across same-day
// reruns, so both the DeriveNanoID(date) id and the slot claims converge.
func futureDayAt(days, hour int) time.Time {
	day := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour).Truncate(24 * time.Hour)
	return day.Add(time.Duration(hour) * time.Hour)
}

// slotClaimKeys enumerates the studio's per-cell slot-claim aspect keys a
// CreateSession span covers (15-minute grid) — the declared optionalReads
// for the double-book guard. Mirrors seed-classic-demo.go's helper.
func slotClaimKeys(hub string, start, end time.Time) []string {
	var keys []string
	for cur := start; cur.Before(end); cur = cur.Add(15 * time.Minute) {
		code := strings.ToLower(strings.NewReplacer("-", "", ":", "").Replace(cur.UTC().Format(time.RFC3339)))
		keys = append(keys, hub+".slot"+code)
	}
	return keys
}

// seedLocationPresentation layers the class-2 display names onto the three
// showcase locations. CreateLocation writes .presentation only when the
// payload carries one, so a world minted before the display-name convention
// landed has nameless locations permanently: nothing else backfills them —
// Refractor projects on CDC, so manifest.me's anchors keep rendering bare
// NanoIDs until the aspect is actually written. SetLocationPresentation is
// the live-world editor for exactly that. Per-mutation idempotent (a
// location whose .presentation is already live is skipped), mirroring
// seedClinicTemplate.
func seedLocationPresentation(ctx context.Context, conn *substrate.Conn, adminKey string) {
	for _, locKey := range showcaseLocationOrder {
		// A location a later increment has not created yet on this world is
		// skipped, not named — its creator writes the presentation itself.
		if !alive(ctx, conn, locKey) {
			continue
		}
		if alive(ctx, conn, locKey+".presentation") {
			continue
		}
		submitOp(ctx, conn, adminKey, "SetLocationPresentation", "location",
			map[string]any{"locationKey": locKey, "presentation": showcaseLocationNames[locKey]},
			&processor.ContextHint{Reads: []string{locKey}})
		fmt.Printf("==> named location:  %s (%s)\n", locKey, showcaseLocationNames[locKey]["name"])
	}
}

// seedStaffWorklistApplication keeps the staff persona's worklist non-empty
// on a fresh world: a vacant third unit plus a signed-but-undecided lease
// application from a walk-in applicant — the actionable staff beat
// (DecideLeaseApplication is frontOfHouse's own verb). The leaseapp vertex is
// the increment's idempotency anchor (its id is caller-supplied; the minted
// applicant identity never needs recovering). A visitor deciding the
// application empties the pane until the next reset — the same
// defacement-bounded model as every other demo write.
func seedStaffWorklistApplication(ctx context.Context, conn *substrate.Conn, adminKey, staffKey string) {
	if !alive(ctx, conn, unit3Key) {
		submitOp(ctx, conn, adminKey, "CreateLocation", "location",
			map[string]any{"locationType": "unit", "locationId": unit3ID,
				"presentation": showcaseLocationNames[unit3Key]}, nil)
		submitOp(ctx, conn, adminKey, "WireContainedIn", "location",
			map[string]any{"child": unit3Key, "parent": buildingKey},
			&processor.ContextHint{Reads: []string{unit3Key, buildingKey}})
	}
	// The landlord read model only projects an application whose unit has a
	// `manages` link (its MATCH walks unit ← manages ← identity); the staff
	// persona doubles as Unit 3's manager. Wired before the application so a
	// fresh world's projection walk finds the whole chain on first sight.
	managesLnk := linkKey(staffKey, "manages", unit3Key)
	if !alive(ctx, conn, managesLnk) {
		submitOp(ctx, conn, adminKey, "AssignUnitOwner", "loftspaceOwnership",
			map[string]any{"landlord": staffKey, "unit": unit3Key},
			&processor.ContextHint{Reads: []string{staffKey, unit3Key}})
	}
	if !alive(ctx, conn, unit3Key+".address") {
		submitOp(ctx, conn, adminKey, "SetUnitAddress", "loftspaceListing",
			map[string]any{"unit": unit3Key, "line1": "12 Riverside Walk", "city": "Riverside",
				"region": "CA", "postal": "92501"},
			&processor.ContextHint{Reads: []string{unit3Key}})
	}
	if !alive(ctx, conn, leaseApp3Key) {
		salt, err := substrate.NewNanoID()
		must(err, "generate applicant claim-key salt")
		reply := submitOp(ctx, conn, adminKey, "CreateUnclaimedIdentity", "identity",
			map[string]any{"name": applicant3Name, "email": applicant3Email,
				"claimKeyHash": mustSHA256Hex("showcase-applicant-" + salt)}, nil)
		applicantKey := reply.PrimaryKey
		submitOp(ctx, conn, adminKey, "CreateLeaseApplication", "leaseapp",
			map[string]any{"applicant": applicantKey, "unit": unit3Key, "leaseAppId": leaseApp3ID,
				"moveInDate":      time.Now().UTC().AddDate(0, 0, 30).Format("2006-01-02"),
				"leaseTermMonths": 12, "requestedRent": 2100},
			&processor.ContextHint{Reads: []string{applicantKey, unit3Key}})
	}
	if !alive(ctx, conn, leaseApp3Key+".signature") {
		submitOp(ctx, conn, adminKey, "SignLease", "leaseapp",
			map[string]any{"leaseAppKey": leaseApp3Key},
			&processor.ContextHint{Reads: []string{leaseApp3Key}})
	}
	fmt.Println("==> staff worklist:  " + leaseApp3Key + " (" + applicant3Name + " → Unit 3, signed, awaiting decision)")
}

// retireLegacyTemplates soft-deletes the two backgroundCheck-classed
// templates seed-edge-demo.go branded as laundry, if still alive. Idempotent
// (best-effort — an already-retired or never-created template is skipped,
// not an error): RetireServiceTemplate itself rejects a second retire, so
// this loader checks liveness first rather than tolerating the rejection.
func retireLegacyTemplates(ctx context.Context, conn *substrate.Conn) {
	adminKey := bootstrap.BootstrapIdentityKey
	for _, key := range legacyMislabeledTemplates {
		if !alive(ctx, conn, key) {
			continue
		}
		submitOp(ctx, conn, adminKey, "RetireServiceTemplate", "service",
			map[string]any{"template": key},
			&processor.ContextHint{Reads: []string{key}})
		fmt.Println("==> retired legacy mislabeled template: " + key)
	}
}

// recoverTenants scans Core KV for the residesIn links into the two fixed
// units to recover the (minted, not fixed) tenant identity keys on an
// idempotent rerun, re-wiring any whose residence was unwired, and prints them
// in the same machine-readable form the from-scratch path does. The staff
// persona is the same recovery one relation over, and ensureStaff owns it —
// including printing FACET_STAFF_NANOID. Returns (tenant1Key, tenant2Key) —
// either may come back "" on a world whose residence link cannot be found at
// all, which the caller must check before using it (the W0 personas hanging
// off tenant1/tenant2 all key on their identity, not their fixed unit).
func recoverTenants(ctx context.Context, conn *substrate.Conn, adminKey string) (string, string) {
	js := conn.JetStream()
	coreKV, err := js.KeyValue(ctx, bootstrap.CoreKVBucket)
	must(err, "open core-kv")
	allKeys, err := pkgverify.ListAllKeys(ctx, coreKV)
	must(err, "list core-kv keys")

	var tenantKeys [2]string
	for i, t := range []struct {
		envVar  string
		unitKey string
	}{
		{"FACET_TENANT1_NANOID", unit1Key},
		{"FACET_TENANT2_NANOID", unit2Key},
	} {
		tenantKey, live := findResidentOf(ctx, coreKV, allKeys, t.unitKey)
		if tenantKey == "" {
			continue
		}
		if !live {
			// Same wedge the staff persona hit: the tenant survives its unwire
			// holding a fixed showcase email, so the residence has to be
			// re-wired rather than re-seeded from a new identity.
			submitOp(ctx, conn, adminKey, "WireResidesIn", "serviceLocation",
				map[string]any{"identity": tenantKey, "location": t.unitKey},
				wireHint(tenantKey, "residesIn", t.unitKey))
			fmt.Printf("==> healed:          re-wired %s residesIn %s (link was tombstoned)\n", tenantKey, t.unitKey)
		}
		fmt.Println(t.envVar + "=" + strings.TrimPrefix(tenantKey, "vtx.identity."))
		tenantKeys[i] = tenantKey
	}
	return tenantKeys[0], tenantKeys[1]
}

// findResidentOf scans for a lnk.identity.<id>.residesIn.unit.<unitId> key
// (service-location's WireResidesIn shape) and returns its source identity key
// plus whether that link is still alive. No exclusion: both showcase tenants
// legitimately hold consumer, so residence recovery never filters on it
// (contrast ensureStaff/ensureMaintenanceTech's holdsRole scans, below).
func findResidentOf(ctx context.Context, coreKV jetstream.KeyValue, allKeys map[string]struct{}, unitKey string) (string, bool) {
	return findLinkedIdentity(ctx, coreKV, allKeys, "residesIn", unitKey, "")
}

// findLinkedIdentity scans for a lnk.identity.<id>.<relation>.<type>.<id> key
// into targetKey and returns its source identity key. Both identity spines
// share this shape, so residence and workplace recovery are the same scan with
// a different relation segment.
//
// A LIVE link always wins. A tombstoned one is still returned, with
// alive=false, because the persona behind it survives the unwire: its email is
// a fixed showcase constant, so treating the dead link as "no persona" and
// minting a replacement collides on the identity index and fails the whole
// seed. The caller re-wires instead (WireWorksAt / WireResidesIn revive a
// tombstoned link).
//
// Candidates are visited in sorted order so a world carrying several dead
// links into the same target resolves identically on every rerun, rather than
// on Go's map iteration order.
//
// excludeIfHoldsRoleKey, when non-empty, skips any live candidate that ALSO
// holds this role — the seed invariant that the canonical staff/maintenance
// persona is staff-ONLY (seedStaff's doc comment: "no consumer role"): once a
// second holder of the scanned role also holds this excluded one (§3.4's Sam
// Okafor, granted frontOfHouse on top of his existing consumer hat), a bare
// holdsRole scan can no longer tell the two apart, and — since candidates are
// visited in a fixed sorted order — would otherwise resolve to whichever
// identity id happens to sort first (the 35ca90f5 mis-resolution). Pass "" to
// opt out (residence recovery, findResidentOf, legitimately targets consumer
// holders and must never exclude them).
func findLinkedIdentity(ctx context.Context, coreKV jetstream.KeyValue, allKeys map[string]struct{}, relation, targetKey, excludeIfHoldsRoleKey string) (string, bool) {
	suffix := "." + relation + "." + strings.TrimPrefix(targetKey, "vtx.")
	candidates := make([]string, 0, 4)
	for k := range allKeys {
		if strings.HasPrefix(k, "lnk.identity.") && strings.HasSuffix(k, suffix) {
			candidates = append(candidates, k)
		}
	}
	sort.Strings(candidates)

	var tombstoned string
	for _, k := range candidates {
		env, err := pkgverify.GetEnvelope(ctx, coreKV, k)
		if err != nil {
			continue
		}
		src := linkSourceIdentity(k)
		if src == "" {
			continue
		}
		if del, _ := env["isDeleted"].(bool); del {
			if tombstoned == "" {
				tombstoned = src
			}
			continue
		}
		if excludeIfHoldsRoleKey != "" {
			excludedEnv, excludedErr := pkgverify.GetEnvelope(ctx, coreKV, linkKey(src, "holdsRole", excludeIfHoldsRoleKey))
			if excludedErr == nil {
				if excludedDel, _ := excludedEnv["isDeleted"].(bool); !excludedDel {
					continue
				}
			}
		}
		return src, true
	}
	return tombstoned, false
}

// findBoundIdentity scans for a lnk.<type>.<id>.identifiedBy.identity.<id>
// key FROM a known, fixed entity key (entityKey) and returns the identity it
// is bound to. The mirror image of findLinkedIdentity above (which recovers
// an unknown identity SOURCE by scanning TO a known target): a
// provider-archetype bind (clinic provider / wellness instructor /
// service-domain serviceprovider) instead runs entity→identity (Contract #1
// §1.1), and the bound entity's own id is the FIXED, checked-in half here —
// it is the identity id that recovery must discover. Existence alone (live
// or tombstoned) is enough: Bind*Identity's own CreateOnly guard aspect never
// releases, so a second bind attempt against an already-claimed entity fails
// regardless of the link's own liveness, and no Unbind op exists to revive
// it — unlike worksAt/residesIn there is nothing to re-wire here.
func findBoundIdentity(allKeys map[string]struct{}, entityKey string) (string, bool) {
	prefix := "lnk." + strings.TrimPrefix(entityKey, "vtx.") + ".identifiedBy.identity."
	candidates := make([]string, 0, 2)
	for k := range allKeys {
		if strings.HasPrefix(k, prefix) {
			candidates = append(candidates, k)
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.Strings(candidates)
	identityID := strings.TrimPrefix(candidates[0], prefix)
	if identityID == "" {
		return "", false
	}
	return "vtx.identity." + identityID, true
}

// linkSourceIdentity derives the source identity key from a 6-segment link key
// (lnk.identity.<id>.<relation>.<type>.<id>), returning "" for any other shape.
//
// The KEY is the only reliable source here, not the document's sourceVertex: a
// tombstone mutation supplies just {isDeleted, data} and step 8 writes the whole
// document, so a tombstoned link retains nothing but isDeleted, key and the
// lastModified* triplet — its class, sourceVertex and targetVertex are gone.
// Reading sourceVertex would silently skip exactly the dead links this recovery
// exists to find.
func linkSourceIdentity(linkKey string) string {
	parts := strings.Split(linkKey, ".")
	if len(parts) != 6 || parts[0] != "lnk" || parts[1] != "identity" || parts[2] == "" {
		return ""
	}
	return "vtx.identity." + parts[2]
}

// alive reports whether key resolves to a live (non-tombstoned) Core KV doc.
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

// findOpMetaByType scans Core KV for an op-meta meta-vertex by operationType
// (no canonicalName aspect, so FindOpMetaByOperationType, not
// FindMetaByCanonical — mirrors seed-edge-demo.go's helper of the same name).
func findOpMetaByType(ctx context.Context, conn *substrate.Conn, operationType string) string {
	js := conn.JetStream()
	coreKV, err := js.KeyValue(ctx, bootstrap.CoreKVBucket)
	must(err, "open core-kv")
	allKeys, err := pkgverify.ListAllKeys(ctx, coreKV)
	must(err, "list core-kv keys")
	opMetaKey, err := pkgverify.FindOpMetaByOperationType(ctx, coreKV, allKeys, operationType)
	must(err, "find "+operationType+" op-meta")
	if opMetaKey == "" {
		fmt.Fprintln(os.Stderr, "FATAL: "+operationType+" op-meta not found — has the owning package been installed against this stack?")
		os.Exit(1)
	}
	return opMetaKey
}

// linkKey builds the deterministic 6-segment link key for "source <relation>
// target" from the two vtx.<type>.<id> endpoint keys (Contract #1 §1.1).
func linkKey(source, relation, target string) string {
	return "lnk." + strings.TrimPrefix(source, "vtx.") + "." + relation + "." + strings.TrimPrefix(target, "vtx.")
}

// wireHint is the ContextHint every service-location Wire* op needs: both
// endpoints as required reads, plus the deterministic link key as an OPTIONAL
// read. The link key is absent on a first wire and tombstoned after an
// Unwire*, so it cannot be a required read — but without it declared the
// script cannot see a tombstone, emits a create, and the re-wire fails
// RevisionConflict.
func wireHint(source, relation, target string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads:         []string{source, target},
		OptionalReads: []string{linkKey(source, relation, target)},
	}
}

// Capability grants land through the Refractor capability-lens projection, and
// on a fresh world (first bring-up, the demo's nightly reset) — especially on a
// small box still digesting the package-install CDC burst — that lag can run
// minutes. Every projection-dependent guard in this seed shares one bounded
// window: an op AuthDenied because the actor's capability doc hasn't caught up
// is retried, and a granted role is polled for until it shows in
// cap.roles.<actor>. Lag past the window, or any other rejection, is fatal.
const (
	projectionLagWindow     = 4 * time.Minute
	projectionRetryInterval = 5 * time.Second
)

func submitOp(ctx context.Context, conn *substrate.Conn, actorKey, operationType, class string, payload map[string]any, hint *processor.ContextHint) *processor.OperationReply {
	deadline := time.Now().Add(projectionLagWindow)
	for {
		// A denied op commits nothing (no tracker), so every attempt is a
		// fresh envelope with its own requestId, evaluated from scratch.
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
		if reply.Status == processor.ReplyStatusAccepted {
			return reply
		}
		if reply.Error != nil && reply.Error.Code == processor.ErrCodeAuthDenied && time.Now().Before(deadline) {
			fmt.Fprintf(os.Stderr, "==> %s: AuthDenied — retrying in %s (capability projection may still be catching up)\n", operationType, projectionRetryInterval)
			time.Sleep(projectionRetryInterval)
			continue
		}
		mustAccepted(reply, operationType)
		return reply
	}
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

// waitForRoleGrant polls actorKey's cap.roles.<actor> projection until it
// carries operationType — Contract #6's async re-projection has no synchronous
// "done" signal, so the only wait is a bounded poll (projectionLagWindow).
func waitForRoleGrant(ctx context.Context, conn *substrate.Conn, actorKey, operationType string) {
	rolesKey, err := capabilitykv.RolesKeyFromActor(actorKey)
	must(err, "derive roles key")
	deadline := time.Now().Add(projectionLagWindow)
	for {
		entry, err := conn.KVGet(ctx, bootstrap.CapabilityKVBucket, rolesKey)
		if err == nil {
			doc, perr := capabilitykv.ParseCapabilityDoc(entry.Value)
			must(perr, "parse "+rolesKey)
			for _, p := range doc.PlatformPermissions {
				if p.OperationType == operationType {
					return
				}
			}
		} else if !errors.Is(err, substrate.ErrKeyNotFound) {
			fmt.Fprintf(os.Stderr, "FATAL poll %s: %v\n", rolesKey, err)
			os.Exit(1)
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "FATAL %s never appeared in %s within %s\n", operationType, rolesKey, projectionLagWindow)
			os.Exit(1)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
