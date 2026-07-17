//go:build ignore

// seed-showcase.go — dev-seed for `make seed-showcase` (edge-showcase-app-
// design.md §7.3): loads a curated two-persona demo world using
// service-domain's real families ({laundry, fitness} among the enum) —
// P7 makes the envelope class the machine truth, so a template's family must
// match what it's presented as, or a completed run of it reads as a
// different real thing to any family-matching consumer (e.g. lease-signing's
// renewals lens).
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
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/cmd/lattice/output"
	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/capabilitykv"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/scripts/pkgverify"
)

// Fixed, checked-in handles (Contract #1 — valid 20-char canonical NanoIDs,
// generated once via substrate.NewNanoID and pinned here) — the idempotency
// seam: a second run recognizes this exact world instead of minting a
// disjoint one.
const (
	buildingID   = "A9jnKK2bGwZNrfHHkLme"
	unit1ID      = "J11XtyS84Tiv16GcC6eE"
	unit2ID      = "eM2RNxs5S5rDFr6i8cfa"
	laundryTplID = "z1vfcNcXdkdyHhJoFz55"
	fitnessTplID = "xeY6h9HU3MYWUuiUZhcA"
	instance1ID  = "w3wX6tCr9EQMDo7zKu6P"

	// CreateLocation mints vtx.<locationType>.<id> — the type-specific prefix,
	// not a generic "location" one (location-domain's ddls.go).
	buildingKey   = "vtx.building." + buildingID
	unit1Key      = "vtx.unit." + unit1ID
	unit2Key      = "vtx.unit." + unit2ID
	laundryTplKey = "vtx.service." + laundryTplID
	fitnessTplKey = "vtx.service." + fitnessTplID

	tenant1Name  = "Riley Chen"
	tenant1Email = "riley.chen@showcase.dev.lattice.local"
	tenant2Name  = "Sam Okafor"
	tenant2Email = "sam.okafor@showcase.dev.lattice.local"
)

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

	if alive(ctx, conn, buildingKey) {
		fmt.Println("==> showcase world already loaded (building", buildingKey, "is alive) — recovering tenant keys, no ops submitted.")
		recoverAndPrint(ctx, conn)
		retireLegacyTemplates(ctx, conn)
		return
	}

	adminKey := bootstrap.BootstrapIdentityKey
	consumerRoleKey := "vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")

	// --- building + two units --------------------------------------------

	submitOp(ctx, conn, adminKey, "CreateLocation", "location",
		map[string]any{"locationType": "building", "locationId": buildingID}, nil)
	fmt.Println("==> building:        " + buildingKey)

	submitOp(ctx, conn, adminKey, "CreateLocation", "location",
		map[string]any{"locationType": "unit", "locationId": unit1ID}, nil)
	submitOp(ctx, conn, adminKey, "WireContainedIn", "location",
		map[string]any{"child": unit1Key, "parent": buildingKey},
		&processor.ContextHint{Reads: []string{unit1Key, buildingKey}})
	fmt.Println("==> unit1:           " + unit1Key + " containedIn building")

	submitOp(ctx, conn, adminKey, "CreateLocation", "location",
		map[string]any{"locationType": "unit", "locationId": unit2ID}, nil)
	submitOp(ctx, conn, adminKey, "WireContainedIn", "location",
		map[string]any{"child": unit2Key, "parent": buildingKey},
		&processor.ContextHint{Reads: []string{unit2Key, buildingKey}})
	fmt.Println("==> unit2:           " + unit2Key + " containedIn building")

	// --- two tenants, each residing in their own unit ---------------------

	tenant1Key := seedTenant(ctx, conn, adminKey, consumerRoleKey, unit1Key, tenant1Name, tenant1Email)
	fmt.Printf("==> tenant1:         %s (%s) residesIn unit1\n", tenant1Key, tenant1Name)
	tenant2Key := seedTenant(ctx, conn, adminKey, consumerRoleKey, unit2Key, tenant2Name, tenant2Email)
	fmt.Printf("==> tenant2:         %s (%s) residesIn unit2\n", tenant2Key, tenant2Name)

	// --- two service templates, correct families, both availableAt the building --

	requestServiceMeta := findRequestServiceOpMeta(ctx, conn)

	seedTemplate(ctx, conn, adminKey, requestServiceMeta, laundryTplID, "laundry",
		map[string]any{"name": "Maple Laundry", "description": "Wash-and-fold, 24h turnaround", "icon": "laundry", "category": "home"})
	fmt.Println("==> template laundry: " + laundryTplKey + " availableAt building")

	seedTemplate(ctx, conn, adminKey, requestServiceMeta, fitnessTplID, "fitness",
		map[string]any{"name": "Riverside Fitness Studio", "description": "Drop-in classes + open gym", "icon": "fitness", "category": "wellness"})
	fmt.Println("==> template fitness: " + fitnessTplKey + " availableAt building")

	// --- one completed instance for tenant1 (the Activity timeline seed) --

	instReply := submitOp(ctx, conn, adminKey, "CreateServiceInstance", "service",
		map[string]any{"family": "laundry", "instanceId": instance1ID, "template": laundryTplKey, "providedTo": tenant1Key},
		&processor.ContextHint{Reads: []string{laundryTplKey, tenant1Key}})
	instKey := instReply.PrimaryKey
	submitOp(ctx, conn, adminKey, "RecordServiceOutcome", "service",
		map[string]any{"instanceKey": instKey, "status": "completed", "completedAt": "2026-07-15T09:00:00Z"},
		&processor.ContextHint{Reads: []string{instKey}})
	fmt.Println("==> instance:        " + instKey + " (laundry, tenant1, completed) — Activity timeline seed")

	// Cold-start race guard (verticals.md "Facet cold-start races the cap
	// projection", ef45e83): wait for both tenants' consumer role grant to
	// project before this loader returns, so an immediate `make up-facet`
	// (or a login attempt) never races cap.roles.<tenant>.
	waitForRoleGrant(ctx, conn, tenant1Key, "ctrl.refractor.register")
	waitForRoleGrant(ctx, conn, tenant2Key, "ctrl.refractor.register")

	retireLegacyTemplates(ctx, conn)

	fmt.Println()
	fmt.Println("==> showcase world seeded.")
	fmt.Println("FACET_TENANT1_NANOID=" + strings.TrimPrefix(tenant1Key, "vtx.identity."))
	fmt.Println("FACET_TENANT2_NANOID=" + strings.TrimPrefix(tenant2Key, "vtx.identity."))
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
		&processor.ContextHint{Reads: []string{tenantKey, unitKey}})
	submitOp(ctx, conn, adminKey, "UpdateIdentityState", "identity",
		map[string]any{"identityKey": tenantKey, "newState": "claimed"},
		&processor.ContextHint{Reads: []string{tenantKey, tenantKey + ".state"}})
	return tenantKey
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
		&processor.ContextHint{Reads: []string{tplKey, buildingKey}})
	submitOp(ctx, conn, adminKey, "WirePermitsOperation", "serviceLocation",
		map[string]any{"service": tplKey, "operation": requestServiceMeta},
		&processor.ContextHint{Reads: []string{tplKey, requestServiceMeta}})
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

// recoverAndPrint scans Core KV for the residesIn links into the two fixed
// units to recover the (minted, not fixed) tenant identity keys on an
// idempotent rerun, and prints them in the same machine-readable form the
// from-scratch path does.
func recoverAndPrint(ctx context.Context, conn *substrate.Conn) {
	js := conn.JetStream()
	coreKV, err := js.KeyValue(ctx, bootstrap.CoreKVBucket)
	must(err, "open core-kv")
	allKeys, err := pkgverify.ListAllKeys(ctx, coreKV)
	must(err, "list core-kv keys")

	tenant1Key := findResidentOf(ctx, coreKV, allKeys, unit1Key)
	tenant2Key := findResidentOf(ctx, coreKV, allKeys, unit2Key)
	if tenant1Key != "" {
		fmt.Println("FACET_TENANT1_NANOID=" + strings.TrimPrefix(tenant1Key, "vtx.identity."))
	}
	if tenant2Key != "" {
		fmt.Println("FACET_TENANT2_NANOID=" + strings.TrimPrefix(tenant2Key, "vtx.identity."))
	}
}

// findResidentOf scans for a live lnk.identity.<id>.residesIn.unit.<unitId>
// key (service-location's WireResidesIn shape) and returns its source
// identity key, or "" if none found.
func findResidentOf(ctx context.Context, coreKV jetstream.KeyValue, allKeys map[string]struct{}, unitKey string) string {
	suffix := ".residesIn." + strings.TrimPrefix(unitKey, "vtx.")
	for k := range allKeys {
		if !strings.HasPrefix(k, "lnk.identity.") || !strings.HasSuffix(k, suffix) {
			continue
		}
		env, err := pkgverify.GetEnvelope(ctx, coreKV, k)
		if err != nil {
			continue
		}
		if del, _ := env["isDeleted"].(bool); del {
			continue
		}
		src, _ := env["sourceVertex"].(string)
		if src != "" {
			return src
		}
	}
	return ""
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

// findRequestServiceOpMeta scans Core KV for the RequestService op-meta
// meta-vertex (no canonicalName aspect, so FindOpMetaByOperationType, not
// FindMetaByCanonical — mirrors seed-edge-demo.go's helper of the same name).
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

// waitForRoleGrant polls actorKey's cap.roles.<actor> projection until it
// carries operationType (mirrors seed-edge-demo.go's helper of the same
// name — Contract #6's async re-projection has no synchronous "done" signal).
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
