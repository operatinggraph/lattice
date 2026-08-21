package main

import "testing"

// The rows below are the `opCatalog` lens's OWN flat RETURN shape
// (packages/edge-manifest/lenses.go, opCatalogSpec) — column names copied from
// its aliases, not invented here. If the lens renames a column, these fixtures
// are what say so: the app's re-nesting is the only place the two spellings
// meet, and a silent mismatch there renders an empty form rather than failing.

func TestComputeOpCatalog_ReNestsTheDescriptorVocabulary(t *testing.T) {
	entries := map[string]string{
		"ResolveWorkOrder": `{"operationType":"ResolveWorkOrder","opMetaKey":"vtx.meta.wo","title":"Resolve a work order",` +
			`"shortLabel":"Resolve","description":"Record what you did.","icon":"wrench","tone":"primary",` +
			`"submitLabel":"Mark resolved","group":"Maintenance",` +
			`"inputSchema":"{\"type\":\"object\",\"properties\":{\"workOrderKey\":{\"type\":\"string\"},\"notes\":{\"type\":\"string\"}},\"required\":[\"workOrderKey\",\"notes\"]}",` +
			`"fieldDescriptions":{"notes":"What you actually did."},` +
			`"dispatchClass":"workOrder","dispatchAuthContext":"task","dispatchTargetField":"workOrderKey",` +
			`"dispatchTargetType":"workorder","dispatchReads":["{payload.workOrderKey}"],` +
			`"dispatchOptionalReads":["{payload.workOrderKey}.resolution"],` +
			`"sensitive":true,"grantedToRoles":["backOfHouse","operator"]}`,
	}

	got := computeOpCatalog(keysOf(entries), fakeKV(entries))
	d, ok := got["ResolveWorkOrder"]
	if !ok {
		t.Fatalf("want a ResolveWorkOrder descriptor, got %+v", got)
	}
	if d.Presentation["title"] != "Resolve a work order" || d.Presentation["submitLabel"] != "Mark resolved" {
		t.Errorf("presentation: %+v", d.Presentation)
	}
	if d.Presentation["shortLabel"] != "Resolve" || d.Presentation["group"] != "Maintenance" {
		t.Errorf("presentation: %+v", d.Presentation)
	}
	if d.InputSchema == "" {
		t.Error("inputSchema must ride through as the declared JSON-schema STRING — the FE parses it")
	}
	if d.FieldDescriptions["notes"] != "What you actually did." {
		t.Errorf("fieldDescriptions: %+v", d.FieldDescriptions)
	}
	if d.Dispatch == nil {
		t.Fatal("dispatch: nil — without it the FE has no class and the envelope is rejected outright")
	}
	if d.Dispatch.Class != "workOrder" || d.Dispatch.AuthContext != "task" {
		t.Errorf("dispatch: %+v", d.Dispatch)
	}
	if d.Dispatch.TargetField != "workOrderKey" || d.Dispatch.TargetType != "workorder" {
		t.Errorf("dispatch target: %+v", d.Dispatch)
	}
	if len(d.Dispatch.Reads) != 1 || d.Dispatch.Reads[0] != "{payload.workOrderKey}" {
		t.Errorf("dispatch reads: %+v", d.Dispatch.Reads)
	}
	if len(d.Dispatch.OptionalReads) != 1 {
		t.Errorf("dispatch optionalReads: %+v", d.Dispatch.OptionalReads)
	}
	if !d.Sensitive {
		t.Error("sensitive must survive — it is the modal's masking rule")
	}
	if len(d.GrantedToRoles) != 2 {
		t.Errorf("grantedToRoles: %+v", d.GrantedToRoles)
	}
}

// A bare op meta projects a row with every descriptor column null. It must
// SURVIVE the proxy: the FE reads "no inputSchema / no dispatch" as
// not-renderable and says so, whereas a dropped row is indistinguishable from a
// projection that has not caught up — and the FE would then silently claim the
// op does not exist.
func TestComputeOpCatalog_KeepsABareRowSoTheFECanDeclineIt(t *testing.T) {
	entries := map[string]string{
		"RecordLeaseDocOutcome": `{"operationType":"RecordLeaseDocOutcome","opMetaKey":"vtx.meta.leg","grantedToRoles":["operator"]}`,
	}
	got := computeOpCatalog(keysOf(entries), fakeKV(entries))
	d, ok := got["RecordLeaseDocOutcome"]
	if !ok {
		t.Fatalf("a bare op meta must still reach the FE, got %+v", got)
	}
	if d.InputSchema != "" || d.Dispatch != nil || d.Presentation != nil {
		t.Errorf("a bare row must carry no descriptor, got %+v", d)
	}
	if d.GrantedToRoles == nil || len(d.GrantedToRoles) != 1 {
		t.Errorf("grantedToRoles: %+v", d.GrantedToRoles)
	}
}

// visibleWhen is the one descriptor column whose LOSS fails OPEN rather than
// degrading: it says "do not offer this op unless the target row is in this
// state", so a proxy that drops it turns a conditionally-offered op into an
// always-offered one — silently, the day a package author first declares one.
// Nothing in the corpus declares one on a migrated op today, which is exactly
// why it needs a test rather than a live surface to notice.
func TestComputeOpCatalog_CarriesVisibleWhenThroughToTheFE(t *testing.T) {
	entries := map[string]string{
		"SettleTab": `{"operationType":"SettleTab","opMetaKey":"vtx.meta.tab",` +
			`"inputSchema":"{\"type\":\"object\",\"properties\":{\"tabKey\":{\"type\":\"string\"}},\"required\":[\"tabKey\"]}",` +
			`"fieldDescriptions":{"tabKey":"Filled from the tab you chose."},` +
			`"dispatchClass":"tab","dispatchAuthContext":"standing","dispatchTargetField":"tabKey",` +
			`"dispatchTargetType":"tab","dispatchVisibleWhen":{"field":"status","equals":"open"}}`,
	}
	got := computeOpCatalog(keysOf(entries), fakeKV(entries))
	d, ok := got["SettleTab"]
	if !ok {
		t.Fatalf("want a SettleTab descriptor, got %+v", got)
	}
	if d.Dispatch == nil {
		t.Fatal("dispatch: nil — the visibility condition rode on it and is now gone")
	}
	if d.Dispatch.VisibleWhen == nil {
		t.Fatal("visibleWhen was dropped by the proxy — the FE would offer this op in every state")
	}
	if d.Dispatch.VisibleWhen.Field != "status" || d.Dispatch.VisibleWhen.Equals != "open" {
		t.Errorf("visibleWhen: %+v", d.Dispatch.VisibleWhen)
	}
}

// A row whose ONLY dispatch content is the visibility condition must still
// carry a dispatch object. Gating the object's construction on the other
// fields alone would drop the condition for exactly the descriptor that has
// nothing else to say — restoring the fail-open by a second route.
func TestComputeOpCatalog_VisibleWhenAloneStillYieldsADispatch(t *testing.T) {
	entries := map[string]string{
		"PauseSeries": `{"operationType":"PauseSeries","dispatchVisibleWhen":{"field":"paused","equals":false}}`,
	}
	got := computeOpCatalog(keysOf(entries), fakeKV(entries))
	d := got["PauseSeries"]
	if d.Dispatch == nil || d.Dispatch.VisibleWhen == nil {
		t.Fatalf("a visibility-only dispatch must survive, got %+v", d)
	}
	if d.Dispatch.VisibleWhen.Equals != false {
		t.Errorf("equals must keep its JSON type (a bool here, not a string): %+v", d.Dispatch.VisibleWhen)
	}
}

// An unreadable key, a torn body, and a row with no operationType are all
// skipped rather than emitted under an empty key — the lens keys on
// operationType, so a row without one is not addressable as an op at all.
func TestComputeOpCatalog_SkipsUnreadableAndKeylessRows(t *testing.T) {
	entries := map[string]string{
		"SignLease": `{"operationType":"SignLease","title":"Sign your lease"}`,
		"torn":      `{`,
		"keyless":   `{"title":"orphan"}`,
	}
	keys := append(keysOf(entries), "absent-from-the-bucket")

	got := computeOpCatalog(keys, fakeKV(entries))
	if len(got) != 1 {
		t.Fatalf("want only the one addressable row, got %+v", got)
	}
	if _, ok := got["SignLease"]; !ok {
		t.Errorf("want SignLease, got %+v", got)
	}
	if _, ok := got[""]; ok {
		t.Error("a row with no operationType must never be emitted under the empty key")
	}
}
