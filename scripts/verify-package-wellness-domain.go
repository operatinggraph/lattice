//go:build ignore

// verify-package-wellness-domain.go — assertion tool for
// `make verify-package-wellness-domain`.
//
// Connects to a running Lattice NATS instance and checks that the
// wellness-domain package has been correctly installed (after its deps
// orchestration-base + service-domain + identity-domain + lease-signing).
// Asserts:
//
//	20 DDLs: studio (CreateStudio + TombstoneStudio), session (CreateSession +
//	  TombstoneSession + ReassignSession + CreateSessionSeries), sessionseries
//	  (CreateSessionSeries), booking (CreateBooking + CancelBooking +
//	  JoinWaitlist + SetBookingAttendance + ReleaseOrphanedBooking +
//	  PromoteWaitlistedBookings), instructor (CreateInstructor +
//	  TombstoneInstructor + SetInstructorProfile + BindInstructorIdentity),
//	  each vertexType, plus 15 aspectType DDLs (studioProfile, sessionSchedule,
//	  studioSlotClaim, instructorSlotClaim, bookerSlotClaim, sessionSeatClaim,
//	  sessionWaitlistClaim, sessionBookerClaim, bookingStatus,
//	  instructorProfile, instructorIdentityClaim, identityInstructorClaim,
//	  sessionSeriesDefinition, wellnessrefund [deliberately Class
//	  meta.ddl.aspectType — ddls.go's comment explains why a vertex-shaped
//	  mutation uses the aspectType Kind], wellnessRefundDetail), each with its
//	  self-description.
//	19 permission vertices: one per (operationType, scope) pair (Contract #8
//	  §8.1). Most ops carry a single scope=any vertex granted to operator;
//	  CreateStudio/CreateSession/CreateSessionSeries additionally grant
//	  frontOfHouse at scope=any (the studio front-desk beat); TombstoneSession/
//	  ReassignSession/SetBookingAttendance additionally grant provider AND
//	  frontOfHouse at scope=any (the bound-instructor / staff split);
//	  CreateBooking/JoinWaitlist/CancelBooking each carry both a scope=any
//	  vertex (operator + frontOfHouse) and a scope=self vertex granted to
//	  consumer (the real-actor-write-auth-e2e idiom, clinic-domain's
//	  CreateAppointment precedent); SetInstructorProfile's scope=any vertex is
//	  ALSO granted to provider (a bound instructor's own profile).
//	9 lens canonicalNames: the six flat projections (wellnessStudios,
//	  wellnessSessions, wellnessBookings, wellnessInstructors, wellnessMembers,
//	  wellnessBookers), the one Protected Postgres lens (wellnessIdentitiesRead),
//	  and the two convergence lenses (wellnessOrphanedBookingSettlement,
//	  wellnessWaitlistPromotion) the weaverTargets below dispatch over.
//	2 meta.weaverTarget playbooks: wellnessOrphanedBookingSettlement
//	  (LensRef wellnessOrphanedBookingSettlement, gap missing_release →
//	  directOp ReleaseOrphanedBooking) and wellnessWaitlistPromotion (LensRef
//	  wellnessWaitlistPromotion, gap missing_promotion → directOp
//	  PromoteWaitlistedBookings) — each asserted for targetId, its installed
//	  lensRef resolving to the matching lens's own NanoID, and its gap key's
//	  action/operation.
//	1 package vertex + manifest aspect (name=wellness-domain).
//
// Run via: go run ./scripts/verify-package-wellness-domain.go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/scripts/pkgverify"
)

const (
	wellnessPackageName  = "wellness-domain"
	wellnessCoreKVBucket = "core-kv"
)

var wellnessExpectedOps = []string{
	"CreateStudio", "TombstoneStudio",
	"CreateSession", "TombstoneSession", "ReassignSession", "CreateSessionSeries",
	"CreateBooking", "JoinWaitlist", "CancelBooking", "SetBookingAttendance",
	"CreateInstructor", "TombstoneInstructor", "SetInstructorProfile", "BindInstructorIdentity",
	"ReleaseOrphanedBooking", "PromoteWaitlistedBookings",
}

// permGrant is one expected (scope, grantee-role) pair for an operationType's
// permission vertex. Mirrors clinic-domain's own permGrant table
// (verify-package-clinic-domain.go) — every op here carries exactly one
// vertex per DISTINCT scope (Contract #8 §8.1), so a widened grantee set on
// the SAME scope lands on the SAME vertex, never a second one.
type permGrant struct {
	scope   string
	grantee string
}

var wellnessOpGrants = map[string][]permGrant{
	"CreateStudio":              {{"any", "operator"}, {"any", "frontOfHouse"}},
	"TombstoneStudio":           {{"any", "operator"}},
	"CreateSession":             {{"any", "operator"}, {"any", "frontOfHouse"}},
	"CreateSessionSeries":       {{"any", "operator"}, {"any", "frontOfHouse"}},
	"TombstoneSession":          {{"any", "operator"}, {"any", "provider"}, {"any", "frontOfHouse"}},
	"ReassignSession":           {{"any", "operator"}, {"any", "frontOfHouse"}, {"any", "provider"}},
	"CreateBooking":             {{"any", "operator"}, {"any", "frontOfHouse"}, {"self", "consumer"}},
	"JoinWaitlist":              {{"any", "operator"}, {"any", "frontOfHouse"}, {"self", "consumer"}},
	"CancelBooking":             {{"any", "operator"}, {"any", "frontOfHouse"}, {"self", "consumer"}},
	"SetBookingAttendance":      {{"any", "operator"}, {"any", "provider"}, {"any", "frontOfHouse"}},
	"CreateInstructor":          {{"any", "operator"}},
	"TombstoneInstructor":       {{"any", "operator"}},
	"SetInstructorProfile":      {{"any", "operator"}, {"any", "provider"}},
	"BindInstructorIdentity":    {{"any", "operator"}},
	"ReleaseOrphanedBooking":    {{"any", "operator"}},
	"PromoteWaitlistedBookings": {{"any", "operator"}},
}

// ddlCheck describes one DDL to verify: its canonical name, its expected meta
// class, and the ops its permittedCommands must contain.
type ddlCheck struct {
	canonical string
	class     string
	ops       []string
}

// expectedLens describes one lens canonicalName + its expected meta class.
type expectedLens struct {
	canonical string
	class     string
}

// expectedWeaverTarget describes one meta.weaverTarget playbook: its
// targetId, the lens canonicalName its lensRef must resolve to, and the
// (gap key, action, operation) the target's one Gaps entry declares.
type expectedWeaverTarget struct {
	targetID  string
	lensRef   string
	gapKey    string
	action    string
	operation string
}

func main() {
	natsURL := pkgverify.EnvOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := pkgverify.EnvOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot load primordial IDs from %s: %v\n", bootstrapJSONPath, err)
		fmt.Fprintln(os.Stderr, "Suggestion: ensure `make up` has completed; lattice.bootstrap.json must exist.")
		os.Exit(1)
	}

	var natsOpts []nats.Option
	if seed := os.Getenv("NATS_NKEY"); seed != "" {
		nkeyOpt, err := nats.NkeyOptionFromSeed(seed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: load NKey seed %q: %v\n", seed, err)
			os.Exit(1)
		}
		natsOpts = append(natsOpts, nkeyOpt)
	} else if creds := os.Getenv("NATS_CREDS"); creds != "" {
		natsOpts = append(natsOpts, nats.UserCredentials(creds))
	}
	nc, err := nats.Connect(natsURL, natsOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot connect to NATS at %s: %v\n", natsURL, err)
		os.Exit(1)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: jetstream context: %v\n", err)
		os.Exit(1)
	}

	coreKV, err := js.KeyValue(ctx, wellnessCoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", wellnessCoreKVBucket, err)
		os.Exit(1)
	}

	allKeys, err := pkgverify.ListAllKeys(ctx, coreKV)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot list Core KV keys: %v\n", err)
		os.Exit(1)
	}

	var failures []string
	okCount := 0
	ok := func(desc string) {
		fmt.Printf("  OK  %s\n", desc)
		okCount++
	}
	fail := func(desc, reason string) {
		msg := fmt.Sprintf("FAIL: %s: %s", desc, reason)
		fmt.Println(" ", msg)
		failures = append(failures, msg)
	}

	fmt.Printf("verify-package-wellness-domain: scanning %d Core KV keys...\n", len(allKeys))

	ddlChecks := []ddlCheck{
		{canonical: "studio", class: "meta.ddl.vertexType", ops: []string{"CreateStudio", "TombstoneStudio"}},
		{canonical: "session", class: "meta.ddl.vertexType", ops: []string{"CreateSession", "TombstoneSession", "ReassignSession", "CreateSessionSeries"}},
		{canonical: "sessionseries", class: "meta.ddl.vertexType", ops: []string{"CreateSessionSeries"}},
		{canonical: "booking", class: "meta.ddl.vertexType", ops: []string{"CreateBooking", "CancelBooking", "JoinWaitlist", "SetBookingAttendance", "ReleaseOrphanedBooking", "PromoteWaitlistedBookings"}},
		{canonical: "instructor", class: "meta.ddl.vertexType", ops: []string{"CreateInstructor", "TombstoneInstructor", "SetInstructorProfile", "BindInstructorIdentity"}},
		{canonical: "studioProfile", class: "meta.ddl.aspectType", ops: []string{"CreateStudio"}},
		{canonical: "sessionSchedule", class: "meta.ddl.aspectType", ops: []string{"CreateSession", "ReassignSession", "CreateSessionSeries"}},
		{canonical: "sessionSeriesDefinition", class: "meta.ddl.aspectType", ops: []string{"CreateSessionSeries"}},
		{canonical: "studioSlotClaim", class: "meta.ddl.aspectType", ops: []string{"CreateSession", "TombstoneSession", "ReassignSession", "CreateSessionSeries"}},
		{canonical: "instructorSlotClaim", class: "meta.ddl.aspectType", ops: []string{"CreateSession", "TombstoneSession", "ReassignSession", "CreateSessionSeries"}},
		{canonical: "bookerSlotClaim", class: "meta.ddl.aspectType", ops: []string{"CreateBooking", "JoinWaitlist", "CancelBooking", "ReleaseOrphanedBooking"}},
		{canonical: "bookingStatus", class: "meta.ddl.aspectType", ops: []string{"CreateBooking", "JoinWaitlist", "CancelBooking", "SetBookingAttendance", "PromoteWaitlistedBookings"}},
		{canonical: "sessionSeatClaim", class: "meta.ddl.aspectType", ops: []string{"CreateBooking", "CancelBooking", "ReleaseOrphanedBooking", "PromoteWaitlistedBookings"}},
		{canonical: "sessionWaitlistClaim", class: "meta.ddl.aspectType", ops: []string{"JoinWaitlist", "CancelBooking", "ReleaseOrphanedBooking", "PromoteWaitlistedBookings"}},
		{canonical: "sessionBookerClaim", class: "meta.ddl.aspectType", ops: []string{"CreateBooking", "JoinWaitlist", "CancelBooking", "ReleaseOrphanedBooking"}},
		{canonical: "instructorProfile", class: "meta.ddl.aspectType", ops: []string{"CreateInstructor", "SetInstructorProfile"}},
		{canonical: "instructorIdentityClaim", class: "meta.ddl.aspectType", ops: []string{"BindInstructorIdentity"}},
		{canonical: "identityInstructorClaim", class: "meta.ddl.aspectType", ops: []string{"BindInstructorIdentity"}},
		{canonical: "wellnessrefund", class: "meta.ddl.aspectType", ops: []string{"CancelBooking", "ReleaseOrphanedBooking", "SetBookingAttendance"}},
		{canonical: "wellnessRefundDetail", class: "meta.ddl.aspectType", ops: []string{"CancelBooking", "ReleaseOrphanedBooking", "SetBookingAttendance"}},
	}

	for _, dc := range ddlChecks {
		ddlKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, dc.canonical)
		if err != nil || ddlKey == "" {
			fail(dc.canonical+" DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", dc.canonical, err))
			continue
		}
		ok(fmt.Sprintf("%s DDL meta-vertex exists: %s", dc.canonical, ddlKey))

		// class + alive.
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, ddlKey); err != nil {
			fail(ddlKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else {
			if cls, _ := env["class"].(string); cls != dc.class {
				fail(ddlKey+" class", fmt.Sprintf("got %q want %q", cls, dc.class))
			} else {
				ok(ddlKey + " class=" + dc.class)
			}
			if isDeleted, _ := env["isDeleted"].(bool); isDeleted {
				fail(ddlKey+" isDeleted", "vertex is tombstoned")
			} else {
				ok(ddlKey + " isDeleted=false")
			}
		}

		// permittedCommands.
		pcKey := ddlKey + ".permittedCommands"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pcKey); err != nil {
			fail(pcKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			cmds := pkgverify.ToStringSlice(data["commands"])
			cmdSet := pkgverify.ToSet(cmds)
			allPresent := true
			for _, op := range dc.ops {
				if !cmdSet[op] {
					fail(pcKey, fmt.Sprintf("missing command %q", op))
					allPresent = false
				}
			}
			if len(cmds) != len(dc.ops) {
				fail(pcKey, fmt.Sprintf("command count=%d want %d", len(cmds), len(dc.ops)))
				allPresent = false
			}
			if allPresent && len(cmds) == len(dc.ops) {
				ok(fmt.Sprintf("%s contains exactly %v", pcKey, dc.ops))
			}
			if err := pkgverify.CheckAspectEnvelope(env, pcKey, ddlKey, "permittedCommands"); err != nil {
				fail(pcKey+" envelope", err.Error())
			} else {
				ok(pcKey + " envelope shape OK")
			}
		}

		// remaining self-description aspects.
		for _, asp := range []string{"canonicalName", "description", "script", "inputSchema", "outputSchema", "fieldDescription", "examples"} {
			k := ddlKey + "." + asp
			if env, err := pkgverify.GetEnvelope(ctx, coreKV, k); err != nil {
				fail(k, fmt.Sprintf("missing: %v", err))
			} else {
				ok(k + " present")
				if err := pkgverify.CheckAspectEnvelope(env, k, ddlKey, asp); err != nil {
					fail(k+" envelope", err.Error())
				} else {
					ok(k + " envelope shape OK")
				}
			}
		}
	}

	// Discover role NanoIDs (operator from bootstrap; others — consumer,
	// frontOfHouse, provider — by scanning vtx.role.*.canonicalName; all three
	// are minted by identity-domain, a wellness-domain dependency).
	operatorRoleID := bootstrap.RoleOperatorID
	roleIDByCanonical := map[string]string{}
	if operatorRoleID != "" {
		roleIDByCanonical["operator"] = operatorRoleID
	}
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.role.") || !strings.HasSuffix(key, ".canonicalName") {
			continue
		}
		env, err := pkgverify.GetEnvelope(ctx, coreKV, key)
		if err != nil {
			continue
		}
		data, _ := env["data"].(map[string]any)
		val, _ := data["value"].(string)
		if val == "" || val == "operator" {
			// operator is a primordial role, sourced exclusively from
			// bootstrap.RoleOperatorID above — never overwritten by a scan
			// match. A dev stack that has been through more than one
			// bootstrap cycle can carry a second, superseded vtx.role
			// vertex still alive with canonicalName=operator (its OWN
			// grantedBy links tombstoned, the vertex itself never
			// cleaned up); since Go's map iteration order is unstable,
			// letting the scan win here would nondeterministically pick
			// the wrong one.
			continue
		}
		parts := strings.Split(key, ".")
		if len(parts) != 4 {
			continue
		}
		roleIDByCanonical[val] = parts[2]
	}

	// permission vertices + scope + grantedBy-role links. Collect ALL matching
	// vertices per op — a single permIDByOp[op] overwrite would pick whichever
	// vertex Go's unstable map iteration visited last, nondeterministically
	// hiding the other (clinic-domain's own verify script fixed the same bug).
	permIDsByOp := map[string][]string{}
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.permission.") {
			continue
		}
		parts := strings.Split(key, ".")
		if len(parts) != 3 {
			continue
		}
		env, err := pkgverify.GetEnvelope(ctx, coreKV, key)
		if err != nil {
			continue
		}
		if isDeleted, _ := env["isDeleted"].(bool); isDeleted {
			continue
		}
		data, _ := env["data"].(map[string]any)
		opType, _ := data["operationType"].(string)
		for _, expected := range wellnessExpectedOps {
			if opType == expected {
				permIDsByOp[opType] = append(permIDsByOp[opType], parts[2])
				break
			}
		}
	}

	for _, op := range wellnessExpectedOps {
		permIDs := permIDsByOp[op]
		if len(permIDs) == 0 {
			fail("vtx.permission.*[operationType="+op+"]", "not found in Core KV")
			continue
		}
		grants := wellnessOpGrants[op]
		// Vertex count = DISTINCT scopes, not len(grants): a permission's
		// identity is its (operationType, scope) pair (Contract #8 §8.1), so
		// two grants sharing a scope land on the SAME vertex via two
		// grantedBy links, never a second vertex.
		wantScopes := map[string]bool{}
		for _, g := range grants {
			wantScopes[g.scope] = true
		}
		if len(permIDs) != len(wantScopes) {
			fail(fmt.Sprintf("vtx.permission.*[operationType=%s]", op),
				fmt.Sprintf("found %d permission vertices, want %d distinct scope(s) (%v)", len(permIDs), len(wantScopes), grants))
			continue
		}
		for _, grant := range grants {
			// Match this grant to the permission vertex among permIDs
			// carrying its scope (each op's grants declare distinct scopes).
			var matchedID string
			for _, permID := range permIDs {
				env, err := pkgverify.GetEnvelope(ctx, coreKV, "vtx.permission."+permID)
				if err != nil {
					continue
				}
				data, _ := env["data"].(map[string]any)
				if scope, _ := data["scope"].(string); scope == grant.scope {
					matchedID = permID
					break
				}
			}
			if matchedID == "" {
				fail(fmt.Sprintf("vtx.permission.*[operationType=%s,scope=%s]", op, grant.scope),
					"not found among discovered permission vertices")
				continue
			}
			permKey := "vtx.permission." + matchedID
			ok(fmt.Sprintf("%s operationType=%s scope=%s", permKey, op, grant.scope))

			granteeRoleID, roleFound := roleIDByCanonical[grant.grantee]
			if !roleFound {
				fail(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<%s>", matchedID, grant.grantee),
					fmt.Sprintf("role %q NanoID not found; cannot verify grant link", grant.grantee))
				continue
			}
			linkKey := "lnk.permission." + matchedID + ".grantedBy.role." + granteeRoleID
			if _, exists := allKeys[linkKey]; !exists {
				fail(linkKey, "grantedBy."+grant.grantee+" link not found")
			} else if lenv, err := pkgverify.GetEnvelope(ctx, coreKV, linkKey); err != nil {
				fail(linkKey, fmt.Sprintf("cannot read: %v", err))
			} else if isDeleted, _ := lenv["isDeleted"].(bool); isDeleted {
				fail(linkKey, "link is tombstoned")
			} else {
				ok(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<%s> exists", matchedID, grant.grantee))
			}
		}
	}

	// Lens canonicalNames. Collect each found lens's bare NanoID (the meta key
	// with its "vtx.meta." prefix trimmed) so the weaverTarget check below can
	// confirm each target's installed lensRef resolves to the matching lens,
	// not merely to SOME NanoID (internal/pkgmgr/build.go's resolveLensRef —
	// a WeaverTarget's LensRef, authored as a lens canonicalName, installs as
	// that lens's in-batch bare NanoID).
	expectedLenses := []expectedLens{
		{canonical: "wellnessStudios", class: "meta.lens"},
		{canonical: "wellnessSessions", class: "meta.lens"},
		{canonical: "wellnessBookings", class: "meta.lens"},
		{canonical: "wellnessInstructors", class: "meta.lens"},
		{canonical: "wellnessMembers", class: "meta.lens"},
		{canonical: "wellnessBookers", class: "meta.lens"},
		{canonical: "wellnessIdentitiesRead", class: "meta.lens"},
		{canonical: "wellnessOrphanedBookingSettlement", class: "meta.lens"},
		{canonical: "wellnessWaitlistPromotion", class: "meta.lens"},
	}
	lensNanoIDByCanonical := map[string]string{}
	for _, el := range expectedLenses {
		lensKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, el.canonical)
		if err != nil || lensKey == "" {
			fail(el.canonical+" lens meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", el.canonical, err))
			continue
		}
		ok(fmt.Sprintf("%s lens meta-vertex exists: %s", el.canonical, lensKey))
		lensNanoIDByCanonical[el.canonical] = strings.TrimPrefix(lensKey, "vtx.meta.")
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, lensKey); err != nil {
			fail(lensKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else if cls, _ := env["class"].(string); cls != el.class {
			fail(lensKey+" class", fmt.Sprintf("got %q want %q", cls, el.class))
		} else {
			ok(lensKey + " class=" + el.class)
		}
	}

	// meta.weaverTarget playbooks: targetId + lensRef (resolved to the
	// matching lens's own NanoID above) + the gap key's action/operation.
	// A meta.weaverTarget carries no .canonicalName aspect (per
	// internal/pkgmgr/build.go) — its identity is the targetId inside its
	// .spec aspect, mirroring verify-package-clinic-reminders.go's own
	// classify-by-class-then-read-the-identifying-aspect approach.
	expectedTargets := []expectedWeaverTarget{
		{
			targetID:  "wellnessOrphanedBookingSettlement",
			lensRef:   "wellnessOrphanedBookingSettlement",
			gapKey:    "missing_release",
			action:    "directOp",
			operation: "ReleaseOrphanedBooking",
		},
		{
			targetID:  "wellnessWaitlistPromotion",
			lensRef:   "wellnessWaitlistPromotion",
			gapKey:    "missing_promotion",
			action:    "directOp",
			operation: "PromoteWaitlistedBookings",
		},
	}
	for _, et := range expectedTargets {
		var targetKey string
		var specData map[string]any
		for key := range allKeys {
			if !strings.HasPrefix(key, "vtx.meta.") || strings.Count(key, ".") != 2 {
				continue
			}
			env, err := pkgverify.GetEnvelope(ctx, coreKV, key)
			if err != nil {
				continue
			}
			if cls, _ := env["class"].(string); cls != "meta.weaverTarget" {
				continue
			}
			specEnv, err := pkgverify.GetEnvelope(ctx, coreKV, key+".spec")
			if err != nil {
				continue
			}
			sd, _ := specEnv["data"].(map[string]any)
			if tid, _ := sd["targetId"].(string); tid == et.targetID {
				targetKey = key
				specData = sd
				break
			}
		}
		if targetKey == "" {
			fail(et.targetID+" meta.weaverTarget", "no meta.weaverTarget with targetId="+et.targetID+" found")
			continue
		}
		ok(fmt.Sprintf("%s meta.weaverTarget exists (targetId in .spec): %s", et.targetID, targetKey))

		wantLensID, lensFound := lensNanoIDByCanonical[et.lensRef]
		if !lensFound {
			fail(targetKey+".spec lensRef", fmt.Sprintf("cannot verify — %q lens was not itself found above", et.lensRef))
		} else if gotLensID, _ := specData["lensRef"].(string); gotLensID != wantLensID {
			fail(targetKey+".spec lensRef", fmt.Sprintf("got %q want %q (the %s lens's own NanoID)", gotLensID, wantLensID, et.lensRef))
		} else {
			ok(fmt.Sprintf("%s.spec lensRef resolves to the %s lens", targetKey, et.lensRef))
		}

		gaps, _ := specData["gaps"].(map[string]any)
		gap, gapFound := gaps[et.gapKey].(map[string]any)
		if !gapFound {
			fail(targetKey+".spec gaps."+et.gapKey, fmt.Sprintf("gap key not found; got keys %v", gaps))
			continue
		}
		ok(fmt.Sprintf("%s.spec gaps.%s exists", targetKey, et.gapKey))
		if action, _ := gap["action"].(string); action != et.action {
			fail(targetKey+".spec gaps."+et.gapKey+".action", fmt.Sprintf("got %q want %q", action, et.action))
		} else {
			ok(fmt.Sprintf("%s.spec gaps.%s.action=%s", targetKey, et.gapKey, et.action))
		}
		if operation, _ := gap["operation"].(string); operation != et.operation {
			fail(targetKey+".spec gaps."+et.gapKey+".operation", fmt.Sprintf("got %q want %q", operation, et.operation))
		} else {
			ok(fmt.Sprintf("%s.spec gaps.%s.operation=%s", targetKey, et.gapKey, et.operation))
		}
	}

	// Package manifest.
	pkgKey, pkgManifestKey, err := pkgverify.FindPackageManifest(ctx, coreKV, allKeys, wellnessPackageName)
	if err != nil || pkgKey == "" {
		fail("wellness-domain package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", wellnessPackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex exists: %s", pkgKey))
		ok(fmt.Sprintf("package manifest exists: %s", pkgManifestKey))
	}
	if pkgManifestKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if name, _ := data["name"].(string); name != wellnessPackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, wellnessPackageName))
			} else {
				ok(pkgManifestKey + " name=wellness-domain")
			}
			if err := pkgverify.CheckAspectEnvelope(env, pkgManifestKey, pkgKey, "manifest"); err != nil {
				fail(pkgManifestKey+" envelope", err.Error())
			} else {
				ok(pkgManifestKey + " envelope shape OK")
			}
		}
	}

	fmt.Println()
	if len(failures) == 0 {
		fmt.Printf("verify-package-wellness-domain: ALL ASSERTIONS PASSED (%d OK)\n", okCount)
		os.Exit(0)
	}
	fmt.Printf("verify-package-wellness-domain: %d FAILURE(S) (%d OK)\n\n", len(failures), okCount)
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nSuggestion: run `make down && make up && make verify-package-wellness-domain` to reinstall from clean state.\n")
	os.Exit(1)
}
