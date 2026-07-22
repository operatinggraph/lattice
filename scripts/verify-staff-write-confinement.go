//go:build ignore

// verify-staff-write-confinement — live proof of facet-staff-worlds F4 against a
// running stack: the SAME staff actor, holding the SAME scope=any grant, is
// accepted at the building it worksAt and denied at one it does not.
//
//	go run ./scripts/verify-staff-write-confinement.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	showcaseBuildingID = "A9jnKK2bGwZNrfHHkLme" // Riverside Building (seed-showcase)
)

func main() {
	ctx := context.Background()
	url := os.Getenv("NATS_URL")
	if url == "" {
		url = "nats://localhost:4222"
	}
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL: url, Name: "verify-staff-confinement", NKeySeedFile: os.Getenv("NATS_NKEY"),
	})
	must(err, "connect")
	defer conn.Close()

	admin := os.Getenv("ADMIN_KEY")
	staff := os.Getenv("STAFF_KEY")
	if admin == "" || staff == "" {
		fatal("set ADMIN_KEY and STAFF_KEY")
	}
	fmt.Printf("admin=%s\nstaff=%s\n\n", admin, staff)

	// --- build a SECOND building the staff actor does not work at -----------
	bID := mustID()
	uID := mustID()
	bKey := "vtx.building." + bID
	uKey := "vtx.unit." + uID

	expect(submit(ctx, conn, admin, "CreateLocation", "location",
		map[string]any{"locationType": "building", "locationId": bID}, nil),
		"accepted", "mint building B")
	expect(submit(ctx, conn, admin, "CreateLocation", "location",
		map[string]any{"locationType": "unit", "locationId": uID}, nil),
		"accepted", "mint unit B")
	expect(submit(ctx, conn, admin, "WireContainedIn", "location",
		map[string]any{"child": uKey, "parent": bKey},
		&processor.ContextHint{Reads: []string{uKey, bKey}}),
		"accepted", "unit B containedIn building B")
	fmt.Printf("building B = %s (staff does NOT worksAt it)\n\n", bKey)

	// --- two lease applications: one per building ---------------------------
	leaseAt := func(unitKey, label string) string {
		id := mustID()
		key := "vtx.leaseapp." + id
		expect(submit(ctx, conn, admin, "CreateLeaseApplication", "leaseapp",
			map[string]any{"leaseAppId": id, "applicant": admin, "unit": unitKey},
			&processor.ContextHint{Reads: []string{admin, unitKey}}),
			"accepted", "mint leaseapp at "+label)
		return key
	}
	// A FRESH unit inside Riverside each run: lease-signing allows one live
	// application per (applicant, unit), so reusing the seeded unit would make
	// this harness single-shot.
	uAID := mustID()
	uAKey := "vtx.unit." + uAID
	riverside := "vtx.building." + showcaseBuildingID
	expect(submit(ctx, conn, admin, "CreateLocation", "location",
		map[string]any{"locationType": "unit", "locationId": uAID}, nil),
		"accepted", "mint a fresh unit")
	expect(submit(ctx, conn, admin, "WireContainedIn", "location",
		map[string]any{"child": uAKey, "parent": riverside},
		&processor.ContextHint{Reads: []string{uAKey, riverside}}),
		"accepted", "that unit containedIn Riverside")

	leaseA := leaseAt(uAKey, "Riverside (the staff's workplace)")
	leaseB := leaseAt(uKey, "building B")
	fmt.Println()

	// --- the three vectors --------------------------------------------------
	openTab := func(actor, lease string) *processor.OperationReply {
		return submit(ctx, conn, actor, "OpenTab", "tab",
			map[string]any{"leaseAppKey": lease},
			&processor.ContextHint{Reads: []string{lease},
				OptionalReads: []string{lease + ".cafeOpenTab"}})
	}

	fmt.Println("=== F4 vectors ===")
	expect(openTab(staff, leaseA), "accepted",
		"[1] staff OpenTab at its OWN workplace (Riverside)")
	expect(openTab(staff, leaseB), "rejected",
		"[2] staff OpenTab at ANOTHER building  <-- the multi-org gate")
	expect(openTab(admin, leaseB), "accepted",
		"[3] operator OpenTab at building B (root stays unconfined)")

	fmt.Println("\nALL VECTORS PASSED")
}

func submit(ctx context.Context, conn *substrate.Conn, actor, opType, class string,
	payload map[string]any, hint *processor.ContextHint) *processor.OperationReply {
	reqID, err := substrate.NewNanoID()
	must(err, "requestId")
	b, err := json.Marshal(payload)
	must(err, "marshal")
	reply, err := output.SubmitOp(ctx, conn, &processor.OperationEnvelope{
		RequestID: reqID, Lane: processor.LaneDefault, OperationType: opType,
		Actor: actor, Class: class,
		SubmittedAt: time.Now().UTC().Format(time.RFC3339),
		Payload:     b, ContextHint: hint,
	})
	must(err, "submit "+opType)
	return reply
}

func expect(reply *processor.OperationReply, want, label string) {
	got := string(reply.Status)
	detail := ""
	if reply.Error != nil {
		detail = fmt.Sprintf(" [%s: %s]", reply.Error.Code, reply.Error.Message)
	}
	if got != want {
		fmt.Printf("  FAIL %-60s got=%s want=%s%s\n", label, got, want, detail)
		os.Exit(1)
	}
	fmt.Printf("  ok   %-60s %s%s\n", label, got, detail)
}

func mustID() string {
	id, err := substrate.NewNanoID()
	must(err, "nanoid")
	return id
}

func must(err error, what string) {
	if err != nil {
		fatal(what + ": " + err.Error())
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "FATAL: "+msg)
	os.Exit(1)
}
