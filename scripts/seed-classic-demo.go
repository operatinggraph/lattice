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
// seed-edge-demo.go's own dev-loop convention — EXCEPT the clinic patient and
// provider, which use fixed, checked-in handles (patientID / providerID
// below, mirroring seed-showcase.go's idempotency seam) because they land in
// a roster shared across every run: an unpinned id let a rerun mint a second
// "Dr. Classic Demo" / "Classic Demo Patient" that the booking picker and
// patient roster then rendered as an indistinguishable duplicate.
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
// seam for the two clinic entities every run must converge on rather than
// duplicate, mirroring seed-showcase.go's convention.
const (
	patientID  = "rQ7RnR5XWyZP2BSropUD"
	providerID = "zJjptLYizx4vDU6KRt9i"
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

	unitReply := submitOp(ctx, conn, adminKey, "CreateLocation", "location",
		map[string]any{"locationType": "unit",
			"presentation": map[string]any{"name": "Unit 1", "icon": "door"}}, nil)
	unitKey := unitReply.PrimaryKey
	fmt.Printf("==> unit:            %s\n", unitKey)

	submitOp(ctx, conn, adminKey, "SetUnitAddress", "loftspaceListing",
		map[string]any{"unit": unitKey, "line1": "12 Classic Demo Ave", "city": "Springfield", "region": "OR", "postal": "97477"},
		&processor.ContextHint{Reads: []string{unitKey}})
	fmt.Println("==> wired:           unit address")

	submitOp(ctx, conn, adminKey, "SetListing", "loftspaceListing",
		map[string]any{"unit": unitKey, "rentAmount": 2200, "rentCurrency": "USD", "bedrooms": 1,
			"availableFrom": "2026-08-01T00:00:00Z", "leaseTermMonths": 12, "status": "available"},
		&processor.ContextHint{Reads: []string{unitKey}})
	fmt.Printf("==> listing:         %s (available)\n", unitKey)

	// DecideLeaseApplication's self-scoped grant is authorized off the
	// landlord's `manages` link (require_manages) — with no landlord persona
	// this world's application can never be decided outside Loupe/operator.
	// Mirrors seed-showcase.go's seedLandlord: a consumer-only identity whose
	// entire authority is the manages link over this unit.
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
	fmt.Printf("==> landlord:        %s manages %s\n", landlordKey, unitKey)

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

	providerKey := "vtx.provider." + providerID
	if !alive(ctx, conn, providerKey) {
		submitOp(ctx, conn, adminKey, "CreateProvider", "provider",
			map[string]any{"fullName": "Dr. Classic Demo", "specialty": "Family Medicine", "providerId": providerID}, nil)
	}
	fmt.Printf("==> provider:        %s\n", providerKey)

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

	// --- Café: tab opened against the same lease ------------------------------

	tabReply := submitOp(ctx, conn, adminKey, "OpenTab", "tab",
		map[string]any{"leaseAppKey": leaseAppKey},
		&processor.ContextHint{
			Reads:         []string{leaseAppKey},
			OptionalReads: []string{leaseAppKey + ".cafeOpenTab"},
		})
	fmt.Printf("==> tab:             %s (open)\n", tabReply.PrimaryKey)

	// Both items are served at the demo unit, which is where the resident
	// lives — the residence chain a Facet browse walk descends binds the unit
	// itself at depth 0, so the catalog is reachable from the first hop.
	menuLocationHint := &processor.ContextHint{Reads: []string{unitKey}}
	menuItemReply := submitOp(ctx, conn, adminKey, "CreateMenuItem", "menuitem",
		map[string]any{"name": "Latte", "priceCents": 450, "locationKey": unitKey}, menuLocationHint)
	fmt.Printf("==> menu item:       %s (Latte, $4.50)\n", menuItemReply.PrimaryKey)
	submitOp(ctx, conn, adminKey, "CreateMenuItem", "menuitem",
		map[string]any{"name": "Croissant", "priceCents": 350, "locationKey": unitKey}, menuLocationHint)
	fmt.Println("==> menu item:       Croissant, $3.50")

	// --- Wellness: studio + bookable session ---------------------------------

	studioReply := submitOp(ctx, conn, adminKey, "CreateStudio", "studio",
		map[string]any{"name": "Classic Demo Studio", "location": unitKey},
		&processor.ContextHint{Reads: []string{unitKey}})
	studioKey := studioReply.PrimaryKey
	fmt.Printf("==> studio:          %s\n", studioKey)

	// Same 15-minute grid the clinic's appointment uses; the wellness DDL
	// rejects an unaligned span with SlotGridViolation.
	sessionStart := time.Now().UTC().Add(24 * time.Hour).Truncate(15 * time.Minute)
	sessionEnd := sessionStart.Add(time.Hour)

	sessionReply := submitOp(ctx, conn, adminKey, "CreateSession", "session",
		map[string]any{
			"studio":   studioKey,
			"name":     "Vinyasa Flow",
			"startsAt": sessionStart.Format(time.RFC3339),
			"endsAt":   sessionEnd.Format(time.RFC3339),
			"capacity": 12,
		},
		&processor.ContextHint{
			Reads: []string{studioKey},
			// The studio's per-cell slot claims: absent until something books
			// the cell, so optional (Contract #2 §2.5 read-before-create).
			OptionalReads: slotClaimKeys(studioKey, sessionStart, sessionEnd),
		})
	fmt.Printf("==> session:         %s (%s, capacity 12)\n", sessionReply.PrimaryKey, sessionStart.Format(time.RFC3339))

	fmt.Println()
	fmt.Println("==> classic vertical demo data seeded.")
	fmt.Printf("    resident:    %s\n", consumerKey)
	fmt.Printf("    landlord:    %s\n", landlordKey)
	fmt.Printf("    lease app:   %s\n", leaseAppKey)
	fmt.Printf("    listing:     %s\n", unitKey)
	fmt.Printf("    appointment: %s\n", apptKey)
	fmt.Printf("    tab:         %s\n", tabReply.PrimaryKey)
	fmt.Printf("    studio:      %s\n", studioKey)
	fmt.Printf("    session:     %s\n", sessionReply.PrimaryKey)
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
