package main

import "testing"

// F25.3a — Author view logic (logic/weaverauthor.js): the draft<->artifact
// field mapping, the params/reads text round-trip, the lens-spec scaffold,
// and the export bundle shape — asserted against the shipped embedded asset.

func TestWeaverAuthorParamsRoundTrip(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	got := call(t, vm, "parseParamsText", "a=1\nb=two\n\nbadline\n c = spaced ").(map[string]any)
	want := map[string]any{"a": "1", "b": "two", "c": "spaced"}
	if len(got) != len(want) {
		t.Fatalf("parseParamsText = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("parseParamsText[%q] = %v, want %v", k, got[k], v)
		}
	}
	text := call(t, vm, "paramsToText", map[string]any{"b": "2", "a": "1"}).(string)
	if text != "a=1\nb=2" {
		t.Errorf("paramsToText = %q, want key-sorted a=1\\nb=2", text)
	}
}

func TestWeaverAuthorReadsRoundTrip(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	got := call(t, vm, "parseReadsText", "row.a, row.b  row.c ,,").([]any)
	want := []string{"row.a", "row.b", "row.c"}
	if len(got) != len(want) {
		t.Fatalf("parseReadsText = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("parseReadsText[%d] = %v, want %q", i, got[i], w)
		}
	}
	if got := call(t, vm, "readsToText", []any{"row.a", "row.b"}); got != "row.a, row.b" {
		t.Errorf("readsToText = %v", got)
	}
	if got := call(t, vm, "readsToText", nil); got != "" {
		t.Errorf("readsToText(nil) = %v, want empty", got)
	}
}

func TestWeaverAuthorBuildTargetContentOmitsEmptyFields(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	draft := map[string]any{
		"targetId": "t1", "lensRef": "l1",
		"gaps": map[string]any{
			"missing_x": map[string]any{
				"action": "directOp", "operation": "Foo", "pattern": "",
				"paramsText": "k=v", "readsText": "row.a",
			},
		},
	}
	got := call(t, vm, "buildTargetContent", draft).(map[string]any)
	if got["targetId"] != "t1" || got["lensRef"] != "l1" {
		t.Fatalf("buildTargetContent = %v", got)
	}
	gaps := got["gaps"].(map[string]any)
	gx := gaps["missing_x"].(map[string]any)
	if gx["action"] != "directOp" || gx["operation"] != "Foo" {
		t.Errorf("gap = %v", gx)
	}
	if _, has := gx["pattern"]; has {
		t.Errorf("gap = %v, an empty optional field must be omitted, not carried as \"\"", gx)
	}
	params := gx["params"].(map[string]any)
	if params["k"] != "v" {
		t.Errorf("params = %v", params)
	}
	reads := gx["reads"].([]any)
	if len(reads) != 1 || reads[0] != "row.a" {
		t.Errorf("reads = %v", reads)
	}
}

// TestWeaverAuthorBuildTargetContentDescription pins both halves of the
// optional prose field: typed prose lands trimmed on the artifact, and a blank
// (or whitespace-only) one is OMITTED rather than carried as "" — the wire
// shape pkgmgr's own `description,omitempty` produces, so a description-less
// draft is byte-identical to one authored before the field existed.
func TestWeaverAuthorBuildTargetContentDescription(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	base := func(desc any) map[string]any {
		d := map[string]any{"targetId": "t1", "lensRef": "l1", "gaps": map[string]any{}}
		if desc != nil {
			d["description"] = desc
		}
		return d
	}
	got := call(t, vm, "buildTargetContent", base("  Every settled tab is posted to the house account.  ")).(map[string]any)
	if got["description"] != "Every settled tab is posted to the house account." {
		t.Errorf("description = %v, want the trimmed prose", got["description"])
	}
	for _, blank := range []any{nil, "", "   \n  "} {
		got = call(t, vm, "buildTargetContent", base(blank)).(map[string]any)
		if _, has := got["description"]; has {
			t.Errorf("buildTargetContent(%q) = %v, a blank description must be omitted", blank, got)
		}
	}
}

// TestWeaverAuthorProposeBlockers pins the client half of the propose gate.
// The server refuses a weaverTarget with no description outright, so the button
// must not offer a submission the server will reject — and every unmet reason
// is named, so a disabled Propose is never a mystery.
func TestWeaverAuthorProposeBlockers(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	passing := map[string]any{
		"targetValidation": map[string]any{"valid": true, "errors": []any{}},
		"lensValidation":   map[string]any{"valid": true, "errors": []any{}},
	}
	described := map[string]any{"targetId": "t1", "description": "Every tab settles."}

	if got := call(t, vm, "proposeBlockers", described, passing).([]any); len(got) != 0 {
		t.Errorf("proposeBlockers(checked, described) = %v, want none", got)
	}

	blank := map[string]any{"targetId": "t1", "description": "   "}
	got := call(t, vm, "proposeBlockers", blank, passing).([]any)
	if len(got) != 1 || !containsSub(got[0].(string), "describe") {
		t.Errorf("proposeBlockers(checked, blank description) = %v, want the description reason", got)
	}

	// Never checked: the check reason comes first, and the description reason
	// still rides along so one pass fixes both.
	got = call(t, vm, "proposeBlockers", blank, nil).([]any)
	if len(got) != 2 || !containsSub(got[0].(string), "checks") {
		t.Errorf("proposeBlockers(unchecked, blank) = %v, want checks-first plus the description reason", got)
	}

	failing := map[string]any{
		"targetValidation": map[string]any{"valid": false, "errors": []any{"bad"}},
		"lensValidation":   map[string]any{"valid": true, "errors": []any{}},
	}
	got = call(t, vm, "proposeBlockers", described, failing).([]any)
	if len(got) != 1 || !containsSub(got[0].(string), "target artifact") {
		t.Errorf("proposeBlockers(invalid target) = %v", got)
	}
}

func TestWeaverAuthorBuildLensContentDefaultsAdapter(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	got := call(t, vm, "buildLensContent", map[string]any{
		"lens": map[string]any{"canonicalName": "l1", "bucket": "weaver-targets", "spec": "MATCH (e) RETURN e"},
	}).(map[string]any)
	if got["adapter"] != "nats-kv" {
		t.Errorf("buildLensContent adapter = %v, want nats-kv default", got["adapter"])
	}
	if got["canonicalName"] != "l1" || got["bucket"] != "weaver-targets" {
		t.Errorf("buildLensContent = %v", got)
	}
}

// TestWeaverAuthorBuildLensContentCanonicalNameFallsBackToLensRef pins the
// view's own displayed default (weaverauthor.js's lensBox): an untouched
// canonicalName field must resolve identically here, so Check/Export never
// diverge from what's shown on screen.
func TestWeaverAuthorBuildLensContentCanonicalNameFallsBackToLensRef(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	got := call(t, vm, "buildLensContent", map[string]any{
		"lensRef": "leaseViolations",
		"lens":    map[string]any{"canonicalName": "", "bucket": "weaver-targets"},
	}).(map[string]any)
	if got["canonicalName"] != "leaseViolations" {
		t.Errorf("canonicalName = %v, want the lensRef fallback", got["canonicalName"])
	}
	// An explicit canonicalName always wins over the fallback.
	got = call(t, vm, "buildLensContent", map[string]any{
		"lensRef": "leaseViolations",
		"lens":    map[string]any{"canonicalName": "customName", "bucket": "weaver-targets"},
	}).(map[string]any)
	if got["canonicalName"] != "customName" {
		t.Errorf("canonicalName = %v, want the explicit value to win", got["canonicalName"])
	}
}

// TestWeaverAuthorScaffoldLensSpec pins §10.2's plain-lens key convention:
// the RETURN's own `AS key` produces the <targetId>.<entityId> composite —
// no ProjectionKind/Output descriptor, which the restricted lens artifact
// kind cannot express at all. gapKeys are already full missing_<gap> column
// names (pkgmgr's WeaverTarget gaps-key convention — the map key IS the
// column, orchestrationguard.go) — the template must use them as-is, never
// re-prefixing into missing_missing_x.
func TestWeaverAuthorScaffoldLensSpec(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	spec := call(t, vm, "scaffoldLensSpec", "leaseComplete", []any{"missing_bgcheck", "missing_signature"}).(string)
	for _, want := range []string{
		"'leaseComplete.' + nanoIdFromKey(e.key) AS key",
		"AS missing_bgcheck",
		"AS missing_signature",
		"AS violating",
		"AS entityKey",
	} {
		if !containsSub(spec, want) {
			t.Errorf("scaffoldLensSpec missing %q in:\n%s", want, spec)
		}
	}
	if containsSub(spec, "missing_missing_") {
		t.Errorf("scaffoldLensSpec double-prefixed an already-full gap key:\n%s", spec)
	}
}

func TestWeaverAuthorValidationBadge(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	valid := call(t, vm, "validationBadge", map[string]any{"valid": true, "errors": []any{}}).(map[string]any)
	if valid["cls"] != "ok" {
		t.Errorf("validationBadge(valid) = %v", valid)
	}
	invalid := call(t, vm, "validationBadge", map[string]any{"valid": false, "errors": []any{"a", "b"}}).(map[string]any)
	if invalid["cls"] != "bad" || invalid["text"] != "2 errors" {
		t.Errorf("validationBadge(invalid) = %v", invalid)
	}
	if got := call(t, vm, "validationBadge", nil).(map[string]any); got["cls"] != "muted" {
		t.Errorf("validationBadge(nil) = %v, want the not-checked muted chip", got)
	}
}

// TestWeaverAuthorExportBundleShape pins the wire shape SubmitCapabilityProposal
// needs per artifact (design §6.4): content is a JSON STRING, not a nested
// object, and — the Studio apply-path fix — target is {mode:"newPackage",
// packageName, newVersion:"0.1.0"}, never the old placeholder {mode:"install"}
// that internal/pkgmgr/capabilityapply.go refuses outright (unrecognized
// target.mode) at the final apply click.
func TestWeaverAuthorExportBundleShape(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	draft := map[string]any{
		"targetId": "t1", "lensRef": "l1",
		"gaps": map[string]any{"missing_x": map[string]any{"action": "directOp", "operation": "Foo"}},
		"lens": map[string]any{"canonicalName": "l1", "adapter": "nats-kv", "bucket": "weaver-targets", "spec": "MATCH (e) RETURN e"},
	}
	check := map[string]any{
		"targetValidation": map[string]any{"valid": true, "errors": []any{}},
		"lensValidation":   map[string]any{"valid": false, "errors": []any{"bad spec"}},
	}
	got := call(t, vm, "exportBundle", draft, check, "because", "freshTok1").(map[string]any)
	artifacts := got["artifacts"].([]any)
	if len(artifacts) != 2 {
		t.Fatalf("exportBundle artifacts = %v, want 2 (draft has a lens spec)", artifacts)
	}
	target := artifacts[0].(map[string]any)
	if target["kind"] != "weaverTarget" {
		t.Errorf("artifacts[0].kind = %v, want weaverTarget", target["kind"])
	}
	if _, ok := target["content"].(string); !ok {
		t.Errorf("artifacts[0].content = %v (%T), want a JSON string", target["content"], target["content"])
	}
	if target["rationale"] != "because" {
		t.Errorf("rationale = %v", target["rationale"])
	}
	tm := target["target"].(map[string]any)
	if tm["mode"] != "newPackage" {
		t.Errorf("target.mode = %v, want newPackage", tm["mode"])
	}
	tPkg, _ := tm["packageName"].(string)
	if tPkg == "" {
		t.Error("target.packageName is empty, want a derived non-empty name")
	}
	if tm["newVersion"] != "0.1.0" {
		t.Errorf("target.newVersion = %v, want 0.1.0", tm["newVersion"])
	}
	tv := target["validation"].(map[string]any)
	if tv["state"] != "valid" {
		t.Errorf("target validation state = %v, want valid", tv["state"])
	}
	lens := artifacts[1].(map[string]any)
	lv := lens["validation"].(map[string]any)
	if lv["state"] != "invalid" || lv["report"] != "bad spec" {
		t.Errorf("lens validation = %v, want invalid/bad spec", lv)
	}
	// The two artifacts apply as two INDEPENDENT packages (propose mints one
	// proposal per artifact) — reusing the same packageName would make the
	// second apply refuse itself against the first's already-installed name.
	lPkg, _ := lens["target"].(map[string]any)["packageName"].(string)
	if lPkg == "" || lPkg == tPkg {
		t.Errorf("lens packageName = %q, target packageName = %q — want distinct, both non-empty", lPkg, tPkg)
	}
}

// TestWeaverAuthorExportBundleTargetOnly pins L2: a draft with no lens spec
// (binding an existing installed lens rather than authoring a new one)
// exports/proposes as a SINGLE weaverTarget artifact — no placeholder lens
// artifact that could only ever compute as invalid.
func TestWeaverAuthorExportBundleTargetOnly(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	draft := map[string]any{
		"targetId": "t1", "lensRef": "aNanoIdOrCanonicalName",
		"gaps": map[string]any{},
		"lens": map[string]any{"canonicalName": "", "adapter": "nats-kv", "bucket": "weaver-targets", "spec": "   "},
	}
	check := map[string]any{"targetValidation": map[string]any{"valid": true, "errors": []any{}}}
	got := call(t, vm, "exportBundle", draft, check, "because", "freshTok2").(map[string]any)
	artifacts := got["artifacts"].([]any)
	if len(artifacts) != 1 {
		t.Fatalf("exportBundle artifacts = %v, want exactly 1 (target-only, blank/whitespace-only lens spec)", artifacts)
	}
	if artifacts[0].(map[string]any)["kind"] != "weaverTarget" {
		t.Errorf("artifacts[0].kind = %v", artifacts[0].(map[string]any)["kind"])
	}
}

// TestWeaverAuthorDraftHasLens pins the has-lens discriminator: only a
// non-blank cypher spec counts as "authoring a new lens".
func TestWeaverAuthorDraftHasLens(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	cases := []struct {
		name string
		lens any
		want bool
	}{
		{"no lens field at all", nil, false},
		{"blank spec", map[string]any{"spec": ""}, false},
		{"whitespace-only spec", map[string]any{"spec": "   \n  "}, false},
		{"real spec", map[string]any{"spec": "MATCH (e) RETURN e"}, true},
	}
	for _, tc := range cases {
		draft := map[string]any{"targetId": "t1"}
		if tc.lens != nil {
			draft["lens"] = tc.lens
		}
		if got := call(t, vm, "draftHasLens", draft); got != tc.want {
			t.Errorf("%s: draftHasLens = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := call(t, vm, "draftHasLens", nil); got != false {
		t.Errorf("draftHasLens(nil) = %v, want false", got)
	}
}

// TestWeaverAuthorBuildApplyTarget pins L1: always newPackage, a non-empty
// packageName derived from targetId+fresh, newVersion 0.1.0, and — the
// PlatformProtectedPackage safety net — a packageName that can never equal a
// protected name BY CONSTRUCTION (internal/pkgmgr/capabilityapply.go's
// twelve-name deny-list, none of which share the weaver-target-/weaver-lens-
// prefix).
func TestWeaverAuthorBuildApplyTarget(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	got := call(t, vm, "buildApplyTarget", "weaverTarget", "leaseComplete", "abc123").(map[string]any)
	if got["mode"] != "newPackage" {
		t.Errorf("mode = %v, want newPackage", got["mode"])
	}
	if got["newVersion"] != "0.1.0" {
		t.Errorf("newVersion = %v, want 0.1.0", got["newVersion"])
	}
	pkg, _ := got["packageName"].(string)
	if !containsSub(pkg, "leasecomplete") || !containsSub(pkg, "abc123") {
		t.Errorf("packageName = %q, want it traceable to both targetId and fresh", pkg)
	}
	for _, protected := range []string{
		"rbac-domain", "control-authz", "privacy-base", "privacy-operator-grant",
		"identity-domain", "identity-hygiene", "objects-base", "console-operator",
		"demo-operator", "capability-author", "augur", "orchestration-base", "semantic-contracts",
	} {
		if pkg == protected {
			t.Fatalf("packageName = %q collides with a PlatformProtectedPackage name", pkg)
		}
	}

	// A different fresh token (a second propose/export click on the SAME
	// targetId, e.g. re-proposing an edited "Load into Author" draft) must
	// derive a DIFFERENT packageName — newPackage fails closed against a name
	// a PRIOR apply already installed.
	got2 := call(t, vm, "buildApplyTarget", "weaverTarget", "leaseComplete", "xyz789").(map[string]any)
	if got["packageName"] == got2["packageName"] {
		t.Errorf("two different fresh tokens derived the SAME packageName %q — re-proposing the same targetId would collide with a prior apply", got["packageName"])
	}

	// The lens artifact's kind must derive a DIFFERENT prefix than the
	// target's, even given the identical targetId+fresh, so a co-authored
	// pair's two independent applies never collide with each other.
	lensTarget := call(t, vm, "buildApplyTarget", "lens", "leaseComplete", "abc123").(map[string]any)
	if lensTarget["packageName"] == got["packageName"] {
		t.Errorf("lens and weaverTarget derived the SAME packageName %q for the same targetId+fresh", got["packageName"])
	}
}

// TestWeaverAuthorHydratedDraftReproposesTargetOnly is the coordinator's
// round-trip proof: hydrateFromProposal loads content.lensRef verbatim, and
// after the bridge adapter fix that value is already a bare NanoID — so a
// re-proposed unedited "Load into Author" draft (a) is target-only (one
// artifact, its lens field never touched — emptyLens()'s blank spec), (b) is
// never blocked by proposeBlockers on a lens verdict it never asked for, and
// (c) carries the SAME NanoID lensRef through untouched in the exported
// content — exactly what weaverauthor.go's resolveWeaverTargetLensRefs then
// passes through with no Core KV lookup at all
// (TestWeaverAuthorPropose_TargetOnlyAlreadyNanoIDLensRefNeedsNoConn).
func TestWeaverAuthorHydratedDraftReproposesTargetOnly(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	const lensID = "aaaaaaaaaaaaaaaaaaaa"
	row := map[string]any{
		"kind":      "weaverTarget",
		"content":   `{"targetId":"leaseComplete","lensRef":"` + lensID + `","description":"Every tab settles.","gaps":{}}`,
		"rationale": "because",
	}
	draft := call(t, vm, "hydrateFromProposal", row).(map[string]any)
	if draft == nil {
		t.Fatal("hydrateFromProposal returned nil")
	}
	if draft["lensRef"] != lensID {
		t.Fatalf("draft lensRef = %v, want %v", draft["lensRef"], lensID)
	}
	if got := call(t, vm, "draftHasLens", draft); got != false {
		t.Fatalf("draftHasLens(hydrated) = %v, want false — hydrateFromProposal leaves the lens panel at emptyLens()", got)
	}

	passing := map[string]any{"targetValidation": map[string]any{"valid": true, "errors": []any{}}}
	if got := call(t, vm, "proposeBlockers", draft, passing).([]any); len(got) != 0 {
		t.Errorf("proposeBlockers(hydrated, passing) = %v, want none — must never be blocked on a lens verdict it never requested", got)
	}

	bundle := call(t, vm, "exportBundle", draft, passing, "because", "freshtoken1").(map[string]any)
	artifacts := bundle["artifacts"].([]any)
	if len(artifacts) != 1 {
		t.Fatalf("artifacts = %v, want exactly 1 (target-only)", artifacts)
	}
	a0 := artifacts[0].(map[string]any)
	if a0["kind"] != "weaverTarget" {
		t.Errorf("artifacts[0].kind = %v", a0["kind"])
	}
	content, _ := a0["content"].(string)
	if !containsSub(content, lensID) {
		t.Errorf("exported content = %s, want the untouched NanoID lensRef %q", content, lensID)
	}
	tgt := a0["target"].(map[string]any)
	if tgt["mode"] != "newPackage" {
		t.Errorf("target.mode = %v, want newPackage", tgt["mode"])
	}
	if pkg, _ := tgt["packageName"].(string); pkg == "" {
		t.Error("target.packageName is empty")
	}
}

func TestWeaverAuthorExportFilename(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	if got := call(t, vm, "exportFilename", "lease Complete!"); got != "weaver-target-lease-Complete-.json" {
		t.Errorf("exportFilename = %v", got)
	}
	if got := call(t, vm, "exportFilename", ""); got != "weaver-target-draft.json" {
		t.Errorf("exportFilename(empty) = %v, want the draft fallback", got)
	}
}

// --- NL-2 — Load into Author (hydrateFromProposal) + Describe (buildAuthoringRequest) --------

// TestWeaverAuthorHydrateFromProposalHappyPath pins the inverse of
// buildTargetContent: every field lands, and a gap's params/reads round-trip
// through paramsToText/readsToText exactly the way the form edits them.
func TestWeaverAuthorHydrateFromProposalHappyPath(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	row := map[string]any{
		"kind": "weaverTarget",
		"content": `{"targetId":"leaseComplete","lensRef":"leaseViolations","description":"Every tab settles.",` +
			`"gaps":{"missing_x":{"action":"directOp","operation":"Foo","params":{"k":"v"},"reads":["row.a"]}}}`,
		"intent":    "a target for lease completion",
		"rationale": "because the lease needs it",
	}
	draft := call(t, vm, "hydrateFromProposal", row).(map[string]any)
	if draft == nil {
		t.Fatal("hydrateFromProposal returned nil on a well-formed weaverTarget row")
	}
	if draft["targetId"] != "leaseComplete" || draft["lensRef"] != "leaseViolations" {
		t.Errorf("draft = %v", draft)
	}
	if draft["description"] != "Every tab settles." {
		t.Errorf("description = %v", draft["description"])
	}
	if draft["rationale"] != "because the lease needs it" {
		t.Errorf("rationale = %v, want the row's rationale", draft["rationale"])
	}
	lens := draft["lens"].(map[string]any)
	if lens["canonicalName"] != "" || lens["adapter"] != "nats-kv" || lens["bucket"] != "weaver-targets" {
		t.Errorf("lens = %v, want emptyLens() untouched", lens)
	}
	gaps := draft["gaps"].(map[string]any)
	gx, ok := gaps["missing_x"].(map[string]any)
	if !ok {
		t.Fatalf("gaps = %v, missing missing_x", gaps)
	}
	if gx["action"] != "directOp" || gx["operation"] != "Foo" {
		t.Errorf("gap = %v", gx)
	}
	if gx["paramsText"] != "k=v" {
		t.Errorf("paramsText = %v, want k=v", gx["paramsText"])
	}
	if gx["readsText"] != "row.a" {
		t.Errorf("readsText = %v, want row.a", gx["readsText"])
	}
	// Fields the artifact omitted (pattern etc.) must land as "", the form's
	// own empty-field convention — never undefined.
	if gx["pattern"] != "" {
		t.Errorf("pattern = %v, want empty string for an omitted field", gx["pattern"])
	}
}

// TestWeaverAuthorHydrateFromProposalWrongKind pins the kind guard: only a
// weaverTarget-kind proposal ever hydrates.
func TestWeaverAuthorHydrateFromProposalWrongKind(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	row := map[string]any{"kind": "lens", "content": `{"canonicalName":"l1"}`}
	if got := call(t, vm, "hydrateFromProposal", row); got != nil {
		t.Errorf("hydrateFromProposal(kind=lens) = %v, want nil", got)
	}
	if got := call(t, vm, "hydrateFromProposal", nil); got != nil {
		t.Errorf("hydrateFromProposal(nil) = %v, want nil", got)
	}
}

// TestWeaverAuthorHydrateFromProposalGarbageContent pins the parse guard:
// unparseable JSON, and JSON that parses to something other than an object,
// both return nil rather than a half-built draft.
func TestWeaverAuthorHydrateFromProposalGarbageContent(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	for _, content := range []string{"{not json", "", `"just a string"`, "42", "null"} {
		row := map[string]any{"kind": "weaverTarget", "content": content}
		if got := call(t, vm, "hydrateFromProposal", row); got != nil {
			t.Errorf("hydrateFromProposal(content=%q) = %v, want nil", content, got)
		}
	}
}

func TestWeaverAuthorBuildAuthoringRequest(t *testing.T) {
	vm := logicVM(t, "weaverauthor.js")
	got := call(t, vm, "buildAuthoringRequest", "  a lens listing active providers  ", "  vtx.meta.abc  ").(map[string]any)
	if got["intent"] != "a lens listing active providers" {
		t.Errorf("intent = %v, want trimmed", got["intent"])
	}
	if got["contextRef"] != "vtx.meta.abc" {
		t.Errorf("contextRef = %v, want trimmed", got["contextRef"])
	}

	got = call(t, vm, "buildAuthoringRequest", "an intent", "").(map[string]any)
	if _, has := got["contextRef"]; has {
		t.Errorf("payload = %v, a blank contextRef must be omitted", got)
	}

	for _, blank := range []string{"", "   \n  "} {
		if got := call(t, vm, "buildAuthoringRequest", blank, ""); got != nil {
			t.Errorf("buildAuthoringRequest(%q) = %v, want nil for a blank intent", blank, got)
		}
	}
}
