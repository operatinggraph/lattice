package main

import "testing"

func TestComputeLensesRoster(t *testing.T) {
	docs := map[string]map[string]any{
		"LensActive0000000000":  {"status": "active"},
		"LensPaused0000000000":  {"status": "paused"},
		"LensLagging0000000000": {"status": "active", "consumerLag": float64(4)},
		// Non-lens keys are excluded.
		"health.processor.p1":       {"component": "processor", "instance": "p1"},
		"health.alerts.security.x":  {"severity": "warning"},
		"health.bootstrap.complete": {},
	}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }
	names := map[string]string{
		"LensActive0000000000": "applicantRoster",
		"LensPaused0000000000": "landlordContact",
	}
	resolve := func(id string) (string, string) { return names[id], "" }
	specs := map[string]lensSpecInfo{
		"LensActive0000000000":  {TargetType: "nats_kv"},
		"LensPaused0000000000":  {TargetType: "postgres", Protected: true},
		"LensLagging0000000000": {TargetType: "postgres", GrantTable: true},
	}
	spec := func(id string) lensSpecInfo { return specs[id] }

	rows := computeLenses(keys, read, resolve, spec)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want 3 lenses", rows)
	}
	// Named rows sort first (by name), unnamed last by id.
	if rows[0].CanonicalName != "applicantRoster" || rows[1].CanonicalName != "landlordContact" || rows[2].CanonicalName != "" {
		t.Errorf("sort order = %q,%q,%q", rows[0].CanonicalName, rows[1].CanonicalName, rows[2].CanonicalName)
	}
	if rows[0].Status != "projecting" || rows[0].TargetType != "nats_kv" || rows[0].Protected {
		t.Errorf("projecting kv row = %+v", rows[0])
	}
	// A paused protected postgres lens is the fail-closed activation gate, not
	// a degraded lens — it renders pending-readpath.
	if rows[1].Status != "pending-readpath" || !rows[1].Protected || rows[1].TargetType != "postgres" {
		t.Errorf("pending-readpath protected row = %+v", rows[1])
	}
	if rows[2].Status != "lagging" || !rows[2].GrantTable || len(rows[2].Issues) == 0 {
		t.Errorf("lagging grant-table row = %+v", rows[2])
	}
}

func TestLensSpecJoin(t *testing.T) {
	envs := map[string]string{
		"vtx.meta.L1.spec": `{"data":{"targetType":"postgres","targetConfig":{"protected":true,"table":"t"}}}`,
		"vtx.meta.L2.spec": `{"data":{"targetType":"nats_kv","targetConfig":{"bucket":"b"}}}`,
	}
	get := func(k string) ([]byte, bool) {
		s, ok := envs[k]
		return []byte(s), ok
	}
	if got := lensSpec(get, "L1"); got.TargetType != "postgres" || !got.Protected || got.GrantTable {
		t.Errorf("L1 spec = %+v", got)
	}
	if got := lensSpec(get, "L2"); got.TargetType != "nats_kv" || got.Protected {
		t.Errorf("L2 spec = %+v", got)
	}
	// Missing spec aspect degrades to the zero info, not an error.
	if got := lensSpec(get, "L3"); got != (lensSpecInfo{}) {
		t.Errorf("missing spec = %+v, want zero", got)
	}
	// Malformed envelope JSON also degrades to zero.
	bad := func(string) ([]byte, bool) { return []byte("not json"), true }
	if got := lensSpec(bad, "LX"); got != (lensSpecInfo{}) {
		t.Errorf("malformed spec = %+v, want zero", got)
	}
}
