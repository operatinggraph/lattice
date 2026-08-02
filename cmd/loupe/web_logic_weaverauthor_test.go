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

// TestWeaverAuthorExportBundleShape pins the wire shape a future F25.3b
// SubmitCapabilityProposal call needs per artifact (design §6.4): content is
// a JSON STRING, not a nested object.
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
	got := call(t, vm, "exportBundle", draft, check, "because", "install").(map[string]any)
	artifacts := got["artifacts"].([]any)
	if len(artifacts) != 2 {
		t.Fatalf("exportBundle artifacts = %v, want 2", artifacts)
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
	if tm["mode"] != "install" {
		t.Errorf("target.mode = %v", tm["mode"])
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
