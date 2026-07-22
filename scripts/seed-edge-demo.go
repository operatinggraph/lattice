//go:build ignore

// seed-edge-demo.go — dev-seed for `make seed-edge-demo`.
//
// edge-showcase-app-design.md Fire 1 (G9): the platform ships zero service
// topology anywhere, so a fresh edge-manifest install has nothing for its
// five Personal Lenses to project. This seeds the minimal demo topology the
// design's own worked example describes: a building containing a unit, a
// demo tenant identity holding `consumer` and residing in the unit, and a
// service template (branded "Maple Laundry" via its .presentation aspect)
// availableAt the building with its RequestService op wired
// permitsOperation. Once seeded, the tenant's edge-manifest lenses populate
// all five manifest.* row kinds (manifest.me, manifest.svc.*, manifest.op.*,
// manifest.task.* — empty here, no task seeded — manifest.inst.* — empty
// until a RequestService lands) and RequestService is submittable under
// authContext.service == the template key.
//
// service-domain's `family` enum is closed to {backgroundCheck, payment}
// (service_instance_test.go's TestCreateServiceTemplate_FamilyOutOfEnum_
// Rejected proves a third family, e.g. "inspection", is rejected) —
// widening it to a literal "laundry" family is a service-domain schema
// change out of this fire's scope (edge-showcase-app-design.md §7 Fire 1
// scopes to the manifest package + vocabulary + the install chain + the
// seed, not a service-domain DDL edit). This seed instead uses the real
// `backgroundCheck` family and brands the template's display metadata
// (name/description/icon/category) as the laundry example — presentation is
// decoupled from the family enum (§3.3), so the manifest's edgeServices row
// renders "Maple Laundry" regardless of which family underlies it.
//
// Every op below is submitted directly over NATS as the bootstrap admin
// actor (already `operator` via the primordial seed) — mirroring
// dev-seed-staff / verify-real-actor-write-auth.go's seedStaff/seedListing
// helpers, not the Gateway. This script seeds TOPOLOGY only: it does not
// itself submit RequestService (that is the consumer-actor demo/e2e's job —
// see internal/edge's edge-manifest Fire 1 e2e test) since RequestService
// must run as the tenant actor, under authContext.service, only after the
// cap.svc.<tenant> projection has settled — a live-projection wait that
// belongs to the thing actually proving the flow, not this one-shot seed.
//
// NOT idempotent: mints a fresh building/unit/tenant/template on every run
// (no dedup key), matching dev-seed-staff's own dev-loop convention.
//
// Run via: make seed-edge-demo (== go run ./scripts/seed-edge-demo.go),
// requires `make install-edge-manifest` already applied to the target stack.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/capabilitykv"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/scripts/pkgverify"
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

	// --- building + unit -----------------------------------------------------

	buildingReply := submitOp(ctx, conn, adminKey, "CreateLocation", "location",
		map[string]any{"locationType": "building",
			"presentation": map[string]any{"name": "Riverside Building", "icon": "building"}}, nil)
	buildingKey := buildingReply.PrimaryKey
	fmt.Printf("==> building:        %s\n", buildingKey)

	unitReply := submitOp(ctx, conn, adminKey, "CreateLocation", "location",
		map[string]any{"locationType": "unit",
			"presentation": map[string]any{"name": "Unit 1", "icon": "door"}}, nil)
	unitKey := unitReply.PrimaryKey
	fmt.Printf("==> unit:            %s\n", unitKey)

	submitOp(ctx, conn, adminKey, "WireContainedIn", "location",
		map[string]any{"child": unitKey, "parent": buildingKey},
		&processor.ContextHint{Reads: []string{unitKey, buildingKey}})
	fmt.Println("==> wired:           unit containedIn building")

	// --- demo tenant ----------------------------------------------------------

	salt, err := substrate.NewNanoID()
	must(err, "generate tenant email salt")
	claimKeyPlaintext := "edge-demo-tenant-" + salt
	claimSum := mustSHA256Hex(claimKeyPlaintext)
	tenantReply := submitOp(ctx, conn, adminKey, "CreateUnclaimedIdentity", "identity",
		map[string]any{
			"name":         "Demo Tenant " + salt[:8],
			"email":        "tenant-" + salt[:8] + "@dev.lattice.local",
			"claimKeyHash": claimSum,
		}, nil)
	tenantKey := tenantReply.PrimaryKey
	fmt.Printf("==> tenant identity: %s\n", tenantKey)

	submitOp(ctx, conn, adminKey, "AssignRole", "",
		map[string]any{"actorKey": tenantKey, "roleKey": consumerRoleKey},
		&processor.ContextHint{Reads: []string{tenantKey, consumerRoleKey}})
	fmt.Printf("==> assigned:        %s holds consumer (%s)\n", tenantKey, consumerRoleKey)

	submitOp(ctx, conn, adminKey, "WireResidesIn", "serviceLocation",
		map[string]any{"identity": tenantKey, "location": unitKey},
		wireHint(tenantKey, "residesIn", unitKey))
	fmt.Println("==> wired:           tenant residesIn unit")

	// facet-app-ux.md §3.0: "Fire 2's seed identity is always pre-claimed" —
	// flip state directly as the admin operator (UpdateIdentityState,
	// scope=any, GrantsTo:[operator]) rather than running the real
	// ClaimIdentity ceremony: that op's own mutation unconditionally
	// (re-)creates the tenant's holdsRole→consumer link (permissions.go's
	// "grant consumer in the same commit" R1), which collides with the
	// AssignRole above (RevisionConflict — a create expecting revision 0
	// on a link this seed already created). UpdateIdentityState only
	// touches .state, so it doesn't touch the role grant at all — correct
	// for seeding topology (not proving the credential-claim ceremony,
	// which the real LoftSpace/Clinic apply flows already cover).
	submitOp(ctx, conn, adminKey, "UpdateIdentityState", "identity",
		map[string]any{"identityKey": tenantKey, "newState": "claimed"},
		&processor.ContextHint{Reads: []string{tenantKey, tenantKey + ".state"}})
	fmt.Println("==> claimed:         tenant identity is now claimed")

	// --- service template (backgroundCheck family, "Maple Laundry" branding) --

	templateReply := submitOp(ctx, conn, adminKey, "CreateServiceTemplate", "service",
		map[string]any{
			"family": "backgroundCheck",
			"presentation": map[string]any{
				"name":        "Maple Laundry",
				"description": "Wash-and-fold, 24h turnaround",
				"icon":        "laundry",
				"category":    "home",
			},
		}, nil)
	templateKey := templateReply.PrimaryKey
	fmt.Printf("==> service template: %s\n", templateKey)

	submitOp(ctx, conn, adminKey, "WireAvailableAt", "serviceLocation",
		map[string]any{"service": templateKey, "location": buildingKey},
		wireHint(templateKey, "availableAt", buildingKey))
	fmt.Println("==> wired:           template availableAt building")

	// --- RequestService op-meta lookup + permitsOperation wiring --------------

	opMetaKey := findRequestServiceOpMeta(ctx, conn)
	fmt.Printf("==> RequestService op-meta: %s\n", opMetaKey)

	submitOp(ctx, conn, adminKey, "WirePermitsOperation", "serviceLocation",
		map[string]any{"service": templateKey, "operation": opMetaKey},
		wireHint(templateKey, "permitsOperation", opMetaKey))
	fmt.Println("==> wired:           template permitsOperation RequestService")

	fmt.Println()
	fmt.Println("==> edge-manifest demo topology seeded.")
	fmt.Printf("    tenant:   %s\n", tenantKey)
	fmt.Printf("    template: %s\n", templateKey)
	fmt.Println("    A subsequent RequestService as the tenant, with authContext.service ==",
		templateKey, ", is now authorized via the cap.svc availability grant once it projects.")

	// `make up-facet` launches `bin/facet` immediately after this script
	// returns, and facet's first act is the `personal.register` control op —
	// gated on `cap.roles.<tenant>` carrying `ctrl.refractor.register`
	// (packages/control-authz/permissions.go's consumer grant), which the
	// AssignRole above only re-projects asynchronously (Contract #6). Wait
	// for it here so a cold `make up-facet` doesn't race the projection and
	// fail facet's registration (verticals.md "Facet cold-start races the
	// cap projection"); the other seed steps above already gave the
	// projection a head start, so this is typically an immediate check.
	waitForRoleGrant(ctx, conn, tenantKey, "ctrl.refractor.register")

	// Machine-readable line for `make up-facet` (facet-app-ux.md §7): the
	// bare NanoID (not the full vtx.identity.<id> key) is what EDGE_IDENTITY_ID
	// and `bin/gateway dev-token -sub <NanoID>` both expect.
	fmt.Printf("FACET_TENANT_NANOID=%s\n", strings.TrimPrefix(tenantKey, "vtx.identity."))
}

// findRequestServiceOpMeta scans Core KV for the RequestService op-meta
// meta-vertex the service-domain package installed. Unlike a DDL/Lens/Role,
// an op-meta carries no canonicalName aspect (internal/pkgmgr/build.go's
// op-meta install loop writes only the vertex root {operationType} + the
// optional descriptor-vocabulary aspects), so it is found by
// FindOpMetaByOperationType (a data.operationType scan), not
// FindMetaByCanonical (which verify-package-edge-manifest.go uses for the
// five LENS metas — canonicalName-bearing, unlike op-metas).
func findRequestServiceOpMeta(ctx context.Context, conn *substrate.Conn) string {
	js := conn.JetStream()
	coreKV, err := js.KeyValue(ctx, bootstrap.CoreKVBucket)
	must(err, "open core-kv")

	allKeys, err := pkgverify.ListAllKeys(ctx, coreKV)
	must(err, "list core-kv keys")

	opMetaKey, err := pkgverify.FindOpMetaByOperationType(ctx, coreKV, allKeys, "RequestService")
	must(err, "find RequestService op-meta")
	if opMetaKey == "" {
		fmt.Fprintln(os.Stderr, "FATAL: RequestService op-meta not found — has `make install-edge-manifest` been run against this stack?")
		os.Exit(1)
	}
	return opMetaKey
}

// submitOp submits an operation as actorKey over NATS (the bootstrap-actor
// setup path, not the Gateway) and fatals on a transport error, mirroring
// verify-real-actor-write-auth.go's helper of the same name.
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

func submitOp(ctx context.Context, conn *substrate.Conn, actorKey, operationType, class string, payload map[string]any, hint *processor.ContextHint) *processor.OperationReply {
	return submitOpWithAuthContext(ctx, conn, actorKey, operationType, class, payload, hint, nil)
}

func submitOpWithAuthContext(ctx context.Context, conn *substrate.Conn, actorKey, operationType, class string, payload map[string]any, hint *processor.ContextHint, authContext *processor.AuthContext) *processor.OperationReply {
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
		AuthContext:   authContext,
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

// waitForRoleGrant polls actorKey's cap.roles.<actor> projection (Refractor's
// capabilityRoles lens re-projects asynchronously after a holdsRole change,
// per Contract #6 — there is no synchronous "projection done" signal) until
// it carries operationType, mirroring verify-real-actor-write-auth.go's
// helper of the same name.
func waitForRoleGrant(ctx context.Context, conn *substrate.Conn, actorKey, operationType string) {
	rolesKey, err := capabilitykv.RolesKeyFromActor(actorKey)
	must(err, "derive roles key")
	deadline := time.Now().Add(5 * time.Second)
	for {
		entry, err := conn.KVGet(ctx, bootstrap.CapabilityKVBucket, rolesKey)
		if err == nil {
			doc, perr := capabilitykv.ParseCapabilityDoc(entry.Value)
			must(perr, "parse "+rolesKey)
			for _, p := range doc.PlatformPermissions {
				if p.OperationType == operationType {
					fmt.Printf("==> projected:       %s: %s in %s (rev=%d)\n", actorKey, operationType, rolesKey, entry.Revision)
					return
				}
			}
		} else if !errors.Is(err, substrate.ErrKeyNotFound) {
			fmt.Fprintf(os.Stderr, "FATAL poll %s: %v\n", rolesKey, err)
			os.Exit(1)
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "FATAL %s never appeared in %s within 5s\n", operationType, rolesKey)
			os.Exit(1)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
