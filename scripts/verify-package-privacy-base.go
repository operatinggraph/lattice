//go:build ignore

// verify-package-privacy-base.go — assertion tool for
// `make verify-package-privacy-base`.
//
// Connects to a running Lattice NATS instance and checks that the
// privacy-base package has been correctly installed — the erasure spine's
// declared shape, not just its presence. Asserts:
//
//	13 DDL meta-vertices (vtx.meta.<NanoID>), each with its class + a
//	.canonicalName aspect + isDeleted=false + (for a non-eventType DDL) a
//	.permittedCommands aspect containing exactly its declared ops:
//	  piiKey (aspectType), shredIdentityKey (vertexType),
//	  privacy.keyShredded (eventType), shredRetentionClassKey (vertexType),
//	  privacy.retentionClassKeyShredded (eventType), erasureRequested
//	  (aspectType), sealIdentityForErasure (vertexType),
//	  privacy.erasureRequested (eventType), purgeIdentityDedupFootprint
//	  (vertexType), privacy.dedupFootprintSwept (eventType), erasure
//	  (aspectType), sealIdentityForErasureComplete (vertexType),
//	  privacy.erasureCompleted (eventType)
//	4 lenses (meta.lens), each with a .canonicalName + .bucket aspect:
//	  shredStatus (privacy-shreds), retentionKeyStatus
//	  (privacy-retention-keys), piiKeyEnvelope (privacy-pii-key-envelopes),
//	  identityErasureResidue (weaver-targets)
//	1 identityErasureComplete meta.weaverTarget over the identityErasureResidue
//	  lens, with its 5 gap actions: missing_credentialResidue → directOp
//	  UnbindIdentityCredentials, missing_dedupResidue → directOp
//	  PurgeIdentityDedupFootprint, missing_vaultDestruction → surface,
//	  missing_projectionNullify → surface, missing_erasureSeal → directOp
//	  SealIdentityForErasureComplete
//	1 identityErasure meta.loomPattern, subjectType=identity, its FOUR
//	  systemOp steps asserted in ORDER — ShredIdentityKey →
//	  SealIdentityForErasure → UnbindIdentityCredentials →
//	  PurgeIdentityDedupFootprint — the single most load-bearing assertion in
//	  this file: a silently reordered or truncated spine is exactly the
//	  failure this gate exists to catch.
//	5 permission vertices (vtx.permission.<NanoID>) — RecordShredFinalization,
//	  RecordRetentionClassShredFinalization, SealIdentityForErasure,
//	  PurgeIdentityDedupFootprint, SealIdentityForErasureComplete — each
//	  scope=any with a grantedBy link to the operator role. (ShredIdentityKey
//	  and ShredRetentionClassKey ship no grant from this package by design —
//	  see permissions.go — so neither is asserted here.)
//	1 package vertex (vtx.package.<NanoID>) + manifest aspect (name=privacy-base)
//
// Run via: go run ./scripts/verify-package-privacy-base.go
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/scripts/pkgverify"
)

const (
	privacyPackageName  = "privacy-base"
	privacyCoreKVBucket = "core-kv"
)

// ddlCheck describes one DDL to verify: its canonical name, its expected meta
// class, and (for a non-eventType DDL) the ops its permittedCommands must
// contain. An eventType DDL declares no PermittedCommands, so its ops list is
// empty — the install path still writes a .permittedCommands aspect with an
// empty commands array (internal/pkgmgr/build.go), so the same check applies.
type ddlCheck struct {
	canonical string
	class     string
	ops       []string
}

var privacyDDLChecks = []ddlCheck{
	{canonical: "piiKey", class: "meta.ddl.aspectType", ops: []string{"ShredIdentityKey", "RecordShredFinalization", "ShredRetentionClassKey", "RecordRetentionClassShredFinalization"}},
	{canonical: "shredIdentityKey", class: "meta.ddl.vertexType", ops: []string{"ShredIdentityKey", "RecordShredFinalization"}},
	{canonical: "privacy.keyShredded", class: "meta.ddl.eventType", ops: nil},
	{canonical: "shredRetentionClassKey", class: "meta.ddl.vertexType", ops: []string{"ShredRetentionClassKey", "RecordRetentionClassShredFinalization"}},
	{canonical: "privacy.retentionClassKeyShredded", class: "meta.ddl.eventType", ops: nil},
	{canonical: "erasureRequested", class: "meta.ddl.aspectType", ops: []string{"SealIdentityForErasure"}},
	{canonical: "sealIdentityForErasure", class: "meta.ddl.vertexType", ops: []string{"SealIdentityForErasure"}},
	{canonical: "privacy.erasureRequested", class: "meta.ddl.eventType", ops: nil},
	{canonical: "purgeIdentityDedupFootprint", class: "meta.ddl.vertexType", ops: []string{"PurgeIdentityDedupFootprint"}},
	{canonical: "privacy.dedupFootprintSwept", class: "meta.ddl.eventType", ops: nil},
	{canonical: "erasure", class: "meta.ddl.aspectType", ops: []string{"SealIdentityForErasureComplete"}},
	{canonical: "sealIdentityForErasureComplete", class: "meta.ddl.vertexType", ops: []string{"SealIdentityForErasureComplete"}},
	{canonical: "privacy.erasureCompleted", class: "meta.ddl.eventType", ops: nil},
}

// lensCheck describes one lens to verify: its canonical name and the NATS-KV
// bucket its .bucket aspect must name.
type lensCheck struct {
	canonical string
	bucket    string
}

var privacyLensChecks = []lensCheck{
	{canonical: "shredStatus", bucket: "privacy-shreds"},
	{canonical: "retentionKeyStatus", bucket: "privacy-retention-keys"},
	{canonical: "piiKeyEnvelope", bucket: "privacy-pii-key-envelopes"},
	{canonical: "identityErasureResidue", bucket: "weaver-targets"},
}

const (
	erasureTargetID  = "identityErasureComplete"
	erasureLensRef   = "identityErasureResidue"
	erasurePatternID = "identityErasure"
)

// erasureGapCheck describes one gap action of the identityErasureComplete
// weaverTarget's .spec.gaps map.
type erasureGapCheck struct {
	column    string
	action    string
	operation string // empty for a "surface" gap
}

var privacyErasureGaps = []erasureGapCheck{
	{column: "missing_credentialResidue", action: "directOp", operation: "UnbindIdentityCredentials"},
	{column: "missing_dedupResidue", action: "directOp", operation: "PurgeIdentityDedupFootprint"},
	{column: "missing_vaultDestruction", action: "surface"},
	{column: "missing_projectionNullify", action: "surface"},
	{column: "missing_erasureSeal", action: "directOp", operation: "SealIdentityForErasureComplete"},
}

// erasurePatternStepOrder is the identityErasure Loom pattern's ORDERED
// operation spine (erasure-orchestration-design.md §5): key destruction
// first, write-path closure second, structural cleanup last. A silently
// reordered or truncated spine is the exact failure this gate exists to
// catch.
var erasurePatternStepOrder = []string{
	"ShredIdentityKey",
	"SealIdentityForErasure",
	"UnbindIdentityCredentials",
	"PurgeIdentityDedupFootprint",
}

// privacyExpectedOps is the set of operationTypes this package grants
// permission for. ShredIdentityKey and ShredRetentionClassKey ship no grant
// from this package by design (permissions.go) and are deliberately absent.
var privacyExpectedOps = []string{
	"RecordShredFinalization",
	"RecordRetentionClassShredFinalization",
	"SealIdentityForErasure",
	"PurgeIdentityDedupFootprint",
	"SealIdentityForErasureComplete",
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

	coreKV, err := js.KeyValue(ctx, privacyCoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", privacyCoreKVBucket, err)
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

	fmt.Printf("verify-package-privacy-base: scanning %d Core KV keys...\n", len(allKeys))

	// -------------------------------------------------------------------------
	// 1. 13 DDL meta-vertices: class, isDeleted, canonicalName aspect,
	//    permittedCommands aspect (exact set).
	// -------------------------------------------------------------------------
	ddlKeyByCanonical := map[string]string{}
	for _, dc := range privacyDDLChecks {
		ddlKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, dc.canonical)
		if err != nil || ddlKey == "" {
			fail(dc.canonical+" DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", dc.canonical, err))
			continue
		}
		ddlKeyByCanonical[dc.canonical] = ddlKey
		ok(fmt.Sprintf("%s DDL meta-vertex exists: %s", dc.canonical, ddlKey))

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

		cnKey := ddlKey + ".canonicalName"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, cnKey); err != nil {
			fail(cnKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if val, _ := data["value"].(string); val != dc.canonical {
				fail(cnKey, fmt.Sprintf("value=%q want %q", val, dc.canonical))
			} else {
				ok(cnKey + " value=" + dc.canonical)
			}
			if err := pkgverify.CheckAspectEnvelope(env, cnKey, ddlKey, "canonicalName"); err != nil {
				fail(cnKey+" envelope", err.Error())
			} else {
				ok(cnKey + " envelope shape OK")
			}
		}

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
	}

	// -------------------------------------------------------------------------
	// 2. 4 lenses: class, isDeleted, canonicalName aspect, bucket aspect.
	// -------------------------------------------------------------------------
	lensIDByCanonical := map[string]string{}
	for _, lc := range privacyLensChecks {
		lensKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, lc.canonical)
		if err != nil || lensKey == "" {
			fail(lc.canonical+" lens meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", lc.canonical, err))
			continue
		}
		lensIDByCanonical[lc.canonical] = strings.TrimPrefix(lensKey, "vtx.meta.")
		ok(fmt.Sprintf("%s lens meta-vertex exists: %s", lc.canonical, lensKey))

		if env, err := pkgverify.GetEnvelope(ctx, coreKV, lensKey); err != nil {
			fail(lensKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else {
			if cls, _ := env["class"].(string); cls != "meta.lens" {
				fail(lensKey+" class", fmt.Sprintf("got %q want meta.lens", cls))
			} else {
				ok(lensKey + " class=meta.lens")
			}
			if isDeleted, _ := env["isDeleted"].(bool); isDeleted {
				fail(lensKey+" isDeleted", "lens vertex is tombstoned")
			} else {
				ok(lensKey + " isDeleted=false")
			}
		}

		bKey := lensKey + ".bucket"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, bKey); err != nil {
			fail(bKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if val, _ := data["value"].(string); val != lc.bucket {
				fail(bKey, fmt.Sprintf("value=%q want %q", val, lc.bucket))
			} else {
				ok(bKey + " value=" + lc.bucket)
			}
		}

		spKey := lensKey + ".spec"
		if _, err := pkgverify.GetEnvelope(ctx, coreKV, spKey); err != nil {
			fail(spKey, fmt.Sprintf("missing: %v", err))
		} else {
			ok(spKey + " present")
		}
	}

	// -------------------------------------------------------------------------
	// 3. identityErasureComplete meta.weaverTarget + identityErasure
	//    meta.loomPattern. Neither carries a .canonicalName aspect
	//    (internal/pkgmgr/build.go writes only a `.spec` aspect for each), so
	//    resolve by scanning root vtx.meta.<NanoID> vertices by class, then
	//    reading the identifier out of the aspect that class actually writes.
	//
	//    A tombstone is a KV entry with isDeleted=true, not a removed key
	//    (internal/processor/step8_commit.go seeds a tombstone's body from the
	//    stored document, so class and data survive verbatim on a deleted
	//    root or spec); both the root and its .spec aspect are checked and
	//    skipped independently. Keys are visited in SORTED order and every
	//    live match is collected before any assertion runs, so the result
	//    does not depend on Go's randomized map iteration order, and a
	//    package rename or reinstall that leaves more than one live match
	//    for the same targetId/patternId is reported as an explicit
	//    ambiguity rather than silently resolved by whichever match the
	//    scan happened to see last.
	// -------------------------------------------------------------------------
	type erasureMetaMatch struct {
		key  string
		data map[string]any
	}
	var targetMatches, patternMatches []erasureMetaMatch

	sortedKeys := make([]string, 0, len(allKeys))
	for key := range allKeys {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)

	for _, key := range sortedKeys {
		if !strings.HasPrefix(key, "vtx.meta.") || strings.Count(key, ".") != 2 {
			continue
		}
		env, err := pkgverify.GetEnvelope(ctx, coreKV, key)
		if err != nil {
			continue
		}
		if isDeleted, _ := env["isDeleted"].(bool); isDeleted {
			continue
		}
		cls, _ := env["class"].(string)
		if cls != "meta.weaverTarget" && cls != "meta.loomPattern" {
			continue
		}
		specEnv, err := pkgverify.GetEnvelope(ctx, coreKV, key+".spec")
		if err != nil {
			continue
		}
		if isDeleted, _ := specEnv["isDeleted"].(bool); isDeleted {
			continue
		}
		sd, _ := specEnv["data"].(map[string]any)
		switch cls {
		case "meta.weaverTarget":
			if tid, _ := sd["targetId"].(string); tid == erasureTargetID {
				targetMatches = append(targetMatches, erasureMetaMatch{key: key, data: sd})
			}
		case "meta.loomPattern":
			if pid, _ := sd["patternId"].(string); pid == erasurePatternID {
				patternMatches = append(patternMatches, erasureMetaMatch{key: key, data: sd})
			}
		}
	}

	switch len(targetMatches) {
	case 0:
		fail(erasureTargetID+" meta.weaverTarget", "no live meta.weaverTarget with targetId="+erasureTargetID+" found")
	case 1:
		key, sd := targetMatches[0].key, targetMatches[0].data
		ok(fmt.Sprintf("%s meta.weaverTarget exists (targetId in .spec): %s", erasureTargetID, key))

		// LensRef installs resolved to the lens's own NanoID
		// (internal/pkgmgr/build.go's resolveLensRef), not the
		// canonicalName it was authored as. An empty wantLensID means the
		// lens itself was not found above (section 2 already failed that);
		// treat it as a hard failure here too rather than letting two
		// independently-missing reads compare equal.
		wantLensID := lensIDByCanonical[erasureLensRef]
		lensRef, _ := sd["lensRef"].(string)
		if wantLensID == "" {
			fail(key+".spec lensRef", fmt.Sprintf("cannot verify — %s lens NanoID unknown (see section 2 failure above); got lensRef=%q", erasureLensRef, lensRef))
		} else if lensRef != wantLensID {
			fail(key+".spec lensRef", fmt.Sprintf("got %q want %q (%s's NanoID)", lensRef, wantLensID, erasureLensRef))
		} else {
			ok(key + ".spec lensRef=" + wantLensID + " (" + erasureLensRef + ")")
		}

		gaps, _ := sd["gaps"].(map[string]any)
		for _, gc := range privacyErasureGaps {
			gap, gapFound := gaps[gc.column].(map[string]any)
			if !gapFound {
				fail(key+".spec.gaps."+gc.column, "gap not declared")
				continue
			}
			if action, _ := gap["action"].(string); action != gc.action {
				fail(key+".spec.gaps."+gc.column+".action", fmt.Sprintf("got %q want %q", action, gc.action))
				continue
			}
			if gc.operation == "" {
				ok(key + ".spec.gaps." + gc.column + ".action=" + gc.action)
				continue
			}
			if opType, _ := gap["operation"].(string); opType != gc.operation {
				fail(key+".spec.gaps."+gc.column+".operation", fmt.Sprintf("got %q want %q", opType, gc.operation))
			} else {
				ok(fmt.Sprintf("%s.spec.gaps.%s = directOp %s", key, gc.column, gc.operation))
			}
		}
	default:
		keys := make([]string, len(targetMatches))
		for i, m := range targetMatches {
			keys[i] = m.key
		}
		fail(erasureTargetID+" meta.weaverTarget", fmt.Sprintf("%d live matches found (ambiguous, want exactly 1): %v", len(targetMatches), keys))
	}

	switch len(patternMatches) {
	case 0:
		fail(erasurePatternID+" meta.loomPattern", "no live meta.loomPattern with patternId="+erasurePatternID+" found")
	case 1:
		key, sd := patternMatches[0].key, patternMatches[0].data
		ok(fmt.Sprintf("%s meta.loomPattern exists (patternId in .spec): %s", erasurePatternID, key))

		if subjType, _ := sd["subjectType"].(string); subjType != "identity" {
			fail(key+".spec subjectType", fmt.Sprintf("got %q want %q", subjType, "identity"))
		} else {
			ok(key + ".spec subjectType=identity")
		}

		// THE LOAD-BEARING ASSERTION: the pattern's steps, in order, must be
		// exactly the four-op erasure spine. A silently reordered or
		// truncated spine leaves an installed stack that looks healthy while
		// an erasure no longer runs to completion.
		steps, _ := sd["steps"].([]any)
		gotOps := make([]string, 0, len(steps))
		gotKinds := make([]string, 0, len(steps))
		for _, s := range steps {
			step, _ := s.(map[string]any)
			op, _ := step["operation"].(string)
			kind, _ := step["kind"].(string)
			gotOps = append(gotOps, op)
			gotKinds = append(gotKinds, kind)
		}
		if len(gotOps) != len(erasurePatternStepOrder) {
			fail("identityErasure pattern step count", fmt.Sprintf("got %d steps %v, want %d %v", len(gotOps), gotOps, len(erasurePatternStepOrder), erasurePatternStepOrder))
		} else {
			orderOK := true
			for i, wantOp := range erasurePatternStepOrder {
				if gotOps[i] != wantOp {
					fail(fmt.Sprintf("identityErasure pattern step[%d]", i), fmt.Sprintf("operation=%q want %q", gotOps[i], wantOp))
					orderOK = false
				}
				if gotKinds[i] != "systemOp" {
					fail(fmt.Sprintf("identityErasure pattern step[%d] kind", i), fmt.Sprintf("got %q want systemOp", gotKinds[i]))
					orderOK = false
				}
			}
			if orderOK {
				ok(fmt.Sprintf("identityErasure pattern steps IN ORDER: %v", erasurePatternStepOrder))
			}
		}
	default:
		keys := make([]string, len(patternMatches))
		for i, m := range patternMatches {
			keys[i] = m.key
		}
		fail(erasurePatternID+" meta.loomPattern", fmt.Sprintf("%d live matches found (ambiguous, want exactly 1): %v", len(patternMatches), keys))
	}

	// -------------------------------------------------------------------------
	// 4. 5 permission vertices + grantedBy link to operator.
	//
	//    Matched on operationType AND declaredBy==privacy-base
	//    (internal/pkgmgr/build.go writes declaredBy: def.Name into every
	//    permission vertex's envelope). Permissions carry no cross-package
	//    uniqueness gate — a sibling package or a runtime CreatePermission
	//    can grant the same operationType to operator — so matching on
	//    operationType alone would let this package's OWN grant go missing
	//    while a look-alike from elsewhere keeps the gate green. Candidates
	//    are collected (not last-wins over map order) so more than one live
	//    privacy-base-declared vertex for the same op — which should never
	//    happen, since install ids are deterministic per (package,
	//    operationType) — is reported as an explicit ambiguity.
	// -------------------------------------------------------------------------
	permMatchesByOp := map[string][]string{}
	for _, key := range sortedKeys {
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
		if declaredBy, _ := data["declaredBy"].(string); declaredBy != privacyPackageName {
			continue
		}
		opType, _ := data["operationType"].(string)
		for _, expected := range privacyExpectedOps {
			if opType == expected {
				permMatchesByOp[opType] = append(permMatchesByOp[opType], parts[2])
				break
			}
		}
	}

	for _, op := range privacyExpectedOps {
		matches := permMatchesByOp[op]
		if len(matches) == 0 {
			fail("vtx.permission.*[operationType="+op+" declaredBy="+privacyPackageName+"]", "not found in Core KV")
			continue
		}
		if len(matches) > 1 {
			fail("vtx.permission.*[operationType="+op+" declaredBy="+privacyPackageName+"]", fmt.Sprintf("%d live matches found (ambiguous, want exactly 1): %v", len(matches), matches))
			continue
		}
		permID := matches[0]
		permKey := "vtx.permission." + permID

		env, err := pkgverify.GetEnvelope(ctx, coreKV, permKey)
		if err != nil {
			fail(permKey+" scope", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if scope, _ := data["scope"].(string); scope != "any" {
				fail(permKey+" scope", fmt.Sprintf("got %q want %q", scope, "any"))
			} else {
				ok(permKey + " operationType=" + op + " declaredBy=" + privacyPackageName + " scope=any")
			}
		}

		if bootstrap.RoleOperatorID == "" {
			fail(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<operator>", permID), "bootstrap.RoleOperatorID not loaded; cannot verify grant link")
			continue
		}
		linkKey := "lnk.permission." + permID + ".grantedBy.role." + bootstrap.RoleOperatorID
		if _, exists := allKeys[linkKey]; !exists {
			fail(linkKey, "grantedBy link not found")
		} else if lenv, err := pkgverify.GetEnvelope(ctx, coreKV, linkKey); err != nil {
			fail(linkKey, fmt.Sprintf("cannot read: %v", err))
		} else if isDeleted, _ := lenv["isDeleted"].(bool); isDeleted {
			fail(linkKey, "link is tombstoned")
		} else {
			ok(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<operator> exists", permID))
		}
	}

	// -------------------------------------------------------------------------
	// 5. Package manifest.
	// -------------------------------------------------------------------------
	pkgKey, pkgManifestKey, err := pkgverify.FindPackageManifest(ctx, coreKV, allKeys, privacyPackageName)
	if err != nil || pkgKey == "" {
		fail("privacy-base package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", privacyPackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex + manifest exist: %s / %s", pkgKey, pkgManifestKey))
	}
	if pkgManifestKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if name, _ := data["name"].(string); name != privacyPackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, privacyPackageName))
			} else {
				ok(pkgManifestKey + " name=privacy-base")
			}
			if err := pkgverify.CheckAspectEnvelope(env, pkgManifestKey, pkgKey, "manifest"); err != nil {
				fail(pkgManifestKey+" envelope", err.Error())
			} else {
				ok(pkgManifestKey + " envelope shape OK")
			}
		}
	}

	// -------------------------------------------------------------------------
	// Final report.
	// -------------------------------------------------------------------------
	fmt.Println()
	if len(failures) == 0 {
		fmt.Printf("verify-package-privacy-base: ALL ASSERTIONS PASSED (%d OK)\n", okCount)
		os.Exit(0)
	}
	fmt.Printf("verify-package-privacy-base: %d FAILURE(S) (%d OK)\n\n", len(failures), okCount)
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nSuggestion: run `make down && make up && make verify-package-rbac && make verify-package-identity && make verify-package-privacy-base` to reinstall from clean state.\n")
	os.Exit(1)
}
