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

// The identityErasure pattern progress logic tier (erasure-orchestration-
// design.md §12 Fire B increment 4) — asserted against the shipped embedded
// asset via the same goja harness.

func TestErasureInFlight(t *testing.T) {
	vm := logicVM(t, "shred.js")

	if call(t, vm, "erasureInFlight", nil, nil) != false {
		t.Error("no shred, no residue = never started, not in flight")
	}
	shredding := map[string]any{"shredded": true}
	if call(t, vm, "erasureInFlight", shredding, nil) != true {
		t.Error("shredded but its own finalization unrecorded = in flight")
	}
	finalized := map[string]any{"shredded": true, "vaultKeyDestroyed": true, "projectionsNullified": true}
	if call(t, vm, "erasureInFlight", finalized, nil) != false {
		t.Error("shred finalized, no residue row yet (pattern not started) = not in flight")
	}
	openResidue := map[string]any{"missing_credentialResidue": true}
	if call(t, vm, "erasureInFlight", finalized, openResidue) != true {
		t.Error("an open residue gap = in flight")
	}
	openSeal := map[string]any{"missing_credentialResidue": false, "missing_dedupResidue": false, "missing_erasureSeal": true}
	if call(t, vm, "erasureInFlight", finalized, openSeal) != true {
		t.Error("every sweep closed but the seal still open = in flight")
	}
	sealed := map[string]any{"missing_credentialResidue": false, "missing_dedupResidue": false, "missing_erasureSeal": false}
	if call(t, vm, "erasureInFlight", finalized, sealed) != false {
		t.Error("every gap closed and sealed = not in flight")
	}
}

func TestErasureSteps(t *testing.T) {
	vm := logicVM(t, "shred.js")

	fresh, ok := call(t, vm, "erasureSteps", nil, nil).([]any)
	if !ok || len(fresh) != 5 {
		t.Fatalf("erasureSteps(nil, nil) = %v, want 5 steps", fresh)
	}
	for i, s := range fresh {
		row := s.(map[string]any)
		if row["done"] != false {
			t.Errorf("step %d (%v) done on no data = %v, want false", i, row["label"], row["done"])
		}
	}

	mid, ok := call(t, vm, "erasureSteps",
		map[string]any{"shredded": true},
		map[string]any{"requestedAt": "2026-08-20T00:00:00Z", "missing_credentialResidue": false, "missing_dedupResidue": true, "missing_erasureSeal": true},
	).([]any)
	if !ok || len(mid) != 5 {
		t.Fatalf("erasureSteps mid-run = %v, want 5 steps", mid)
	}
	wantDone := []bool{true, true, true, false, false}
	for i, s := range mid {
		row := s.(map[string]any)
		if row["done"] != wantDone[i] {
			t.Errorf("step %d (%v) done = %v, want %v", i, row["label"], row["done"], wantDone[i])
		}
	}
}
