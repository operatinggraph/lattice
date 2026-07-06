package main

import "testing"

// The Vault-page shred-status logic tier (F12 increment 1): the fleet
// summary and per-row finalization line — asserted against the shipped
// embedded asset via the goja harness.

func TestShredInFlight(t *testing.T) {
	vm := logicVM(t, "shred.js")

	if call(t, vm, "shredInFlight", nil) != true {
		t.Error("nil row = in flight (fail-open to visible, never silently done)")
	}
	if call(t, vm, "shredInFlight", map[string]any{}) != true {
		t.Error("empty row = in flight")
	}
	if call(t, vm, "shredInFlight", map[string]any{"vaultKeyDestroyed": true}) != true {
		t.Error("only vaultKeyDestroyed = still in flight")
	}
	if call(t, vm, "shredInFlight", map[string]any{"projectionsNullified": true}) != true {
		t.Error("only projectionsNullified = still in flight")
	}
	if call(t, vm, "shredInFlight", map[string]any{
		"vaultKeyDestroyed": true, "projectionsNullified": true,
	}) != false {
		t.Error("both steps recorded = not in flight")
	}
}

func TestShredFleetSummary(t *testing.T) {
	vm := logicVM(t, "shred.js")

	if got := call(t, vm, "shredFleetSummary", nil); got != "0 identities shredded · 0 shreds in flight (finalization pending)" {
		t.Errorf("nil rows = %v", got)
	}
	rows := []any{
		map[string]any{"identityKey": "a", "vaultKeyDestroyed": true, "projectionsNullified": true},
		map[string]any{"identityKey": "b", "vaultKeyDestroyed": true},
		map[string]any{"identityKey": "c"},
	}
	if got := call(t, vm, "shredFleetSummary", rows); got != "3 identities shredded · 2 shreds in flight (finalization pending)" {
		t.Errorf("mixed rows = %v", got)
	}
	one := []any{map[string]any{"identityKey": "a"}}
	if got := call(t, vm, "shredFleetSummary", one); got != "1 identity shredded · 1 shred in flight (finalization pending)" {
		t.Errorf("singular row = %v, want singular grammar", got)
	}
}

func TestShredFinalizationLine(t *testing.T) {
	vm := logicVM(t, "shred.js")

	if got := call(t, vm, "shredFinalizationLine", nil); got != "vaultKeyDestroyed … · projectionsNullified …" {
		t.Errorf("nil row = %v", got)
	}
	if got := call(t, vm, "shredFinalizationLine", map[string]any{"vaultKeyDestroyed": true}); got != "vaultKeyDestroyed ✓ · projectionsNullified …" {
		t.Errorf("partial row = %v", got)
	}
	if got := call(t, vm, "shredFinalizationLine", map[string]any{
		"vaultKeyDestroyed": true, "projectionsNullified": true,
	}); got != "vaultKeyDestroyed ✓ · projectionsNullified ✓" {
		t.Errorf("complete row = %v", got)
	}
}
