package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// specEnv wraps a lens spec data object into the Core KV aspect envelope shape
// readLensFullSpec parses.
func specEnv(data map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"data": data})
	return b
}

func TestReadLensFullSpec(t *testing.T) {
	envs := map[string][]byte{
		"vtx.meta.L1.spec": specEnv(map[string]any{
			"engine":       "full",
			"cypherRule":   "MATCH (n) RETURN n.key AS key",
			"targetType":   "nats_kv",
			"outputSchema": map[string]any{"type": "object"},
			"targetConfig": map[string]any{"bucket": "weaver-targets", "key": []any{"key"}},
		}),
		"vtx.meta.L2.spec": specEnv(map[string]any{
			"engine":         "full",
			"projectionKind": "actorAggregate",
			"targetType":     "postgres",
			"targetConfig": map[string]any{
				"dsn": "postgres://secret", "table": "cap_read", "key": "actor_id",
				"protected": true, "deleteMode": "soft",
			},
		}),
	}
	get := func(k string) ([]byte, bool) { v, ok := envs[k]; return v, ok }

	s1 := readLensFullSpec(get, "L1")
	if !s1.Found || s1.Engine != "full" || s1.TargetType != "nats_kv" || s1.CypherRule == "" {
		t.Errorf("L1 spec = %+v", s1)
	}
	if s1.specInfo() != (lensSpecInfo{TargetType: "nats_kv"}) {
		t.Errorf("L1 specInfo = %+v", s1.specInfo())
	}
	s2 := readLensFullSpec(get, "L2")
	if !s2.Found || s2.ProjectionKind != "actorAggregate" {
		t.Errorf("L2 spec = %+v", s2)
	}
	if info := s2.specInfo(); !info.Protected || info.TargetType != "postgres" {
		t.Errorf("L2 specInfo = %+v", info)
	}
	if s3 := readLensFullSpec(get, "L3"); s3.Found {
		t.Errorf("missing spec reported Found: %+v", s3)
	}
}

// TestRenderLensTarget pins the honest-render rule: bucket/table/key/
// deleteMode/posture pass through, the DSN NEVER does — only the fact one is
// configured.
func TestRenderLensTarget(t *testing.T) {
	pg := renderLensTarget("postgres", map[string]any{
		"dsn": "postgres://user:pass@host/db", "table": "cap_read",
		"key": "actor_id", "protected": true,
	})
	if _, leaked := pg["dsn"]; leaked {
		t.Fatal("DSN leaked into the rendered target")
	}
	for _, v := range pg {
		if s, ok := v.(string); ok && s == "postgres://user:pass@host/db" {
			t.Fatal("DSN value leaked under another field")
		}
	}
	if pg["dsnConfigured"] != true || pg["table"] != "cap_read" || pg["protected"] != true {
		t.Errorf("pg target = %+v", pg)
	}
	if !reflect.DeepEqual(pg["keyColumns"], []string{"actor_id"}) {
		t.Errorf("scalar key not normalized: %+v", pg["keyColumns"])
	}

	// Empty-DSN postgres reports dsnConfigured=false (resolved from env).
	pgEnv := renderLensTarget("postgres", map[string]any{"table": "t"})
	if pgEnv["dsnConfigured"] != false {
		t.Errorf("env-dsn target = %+v", pgEnv)
	}

	kv := renderLensTarget("nats_kv", map[string]any{
		"bucket": "weaver-targets", "key": []any{"team_id", "agreement_id"}, "deleteMode": "hard",
	})
	if kv["bucket"] != "weaver-targets" || kv["deleteMode"] != "hard" {
		t.Errorf("kv target = %+v", kv)
	}
	if !reflect.DeepEqual(kv["keyColumns"], []string{"team_id", "agreement_id"}) {
		t.Errorf("array key = %+v", kv["keyColumns"])
	}
	if _, present := kv["dsnConfigured"]; present {
		t.Error("nats_kv target carries dsnConfigured")
	}
}

func TestFindOwningPackage(t *testing.T) {
	manifest := func(name, version string, declared ...string) []byte {
		keys := make([]any, len(declared))
		for i, d := range declared {
			keys[i] = d
		}
		return specEnv(map[string]any{"name": name, "version": version, "declaredKeys": keys})
	}
	envs := map[string][]byte{
		"vtx.package.P1.manifest": manifest("loftspace", "1.2.0", "vtx.meta.LensA", "vtx.role.R1"),
		"vtx.package.P2.manifest": manifest("clinic", "0.9.0", "vtx.meta.LensB"),
	}
	get := func(k string) ([]byte, bool) { v, ok := envs[k]; return v, ok }
	keys := []string{"vtx.package.P2.manifest", "vtx.package.P1.manifest", "vtx.meta.LensA", "lnk.a.b.c.d.e"}

	pkg := findOwningPackage(keys, get, "LensA")
	if pkg == nil || pkg.Key != "vtx.package.P1" || pkg.Name != "loftspace" || pkg.Version != "1.2.0" {
		t.Errorf("LensA owner = %+v", pkg)
	}
	if pkg := findOwningPackage(keys, get, "LensB"); pkg == nil || pkg.Name != "clinic" {
		t.Errorf("LensB owner = %+v", pkg)
	}
	// A kernel (bootstrap-seeded) lens is claimed by no package.
	if pkg := findOwningPackage(keys, get, "KernelLens"); pkg != nil {
		t.Errorf("kernel lens claimed by %+v", pkg)
	}
}

func TestRefractorLensOverlay(t *testing.T) {
	docs := map[string]map[string]any{
		// Older instance: stale lag figure, but the only latency report.
		"health.refractor.r-old": {
			"heartbeatAt": "2026-07-02T10:00:00Z",
			"metrics": map[string]any{
				"lensLags": map[string]any{"applicantRoster": float64(9)},
				"lensLatency": map[string]any{
					"applicantRoster": map[string]any{"count": float64(4), "meanNs": float64(1.5e6)},
				},
			},
		},
		// Freshest instance wins per metric — it carries the lag but NOT the
		// latency, which only the older instance reports.
		"health.refractor.r-new": {
			"heartbeatAt": "2026-07-02T12:00:00Z",
			"metrics": map[string]any{
				"lensLags": map[string]any{"applicantRoster": float64(2)},
			},
		},
		// Non-refractor components and event keys are ignored.
		"health.processor.p1":       {"metrics": map[string]any{"lensLags": map[string]any{"applicantRoster": float64(99)}}},
		"health.refractor.r1.event": {"kind": "event"},
	}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }

	got := refractorLensOverlay(keys, read, "LensId000", "applicantRoster")
	if got == nil {
		t.Fatal("no overlay found")
	}
	if got["lag"] != float64(2) {
		t.Errorf("lag = %v, want the freshest instance's 2", got["lag"])
	}
	// The merge is per metric: the latency survives even though its reporter
	// is not the freshest instance.
	if lat, ok := got["latency"].(map[string]any); !ok || lat["count"] != float64(4) {
		t.Errorf("latency = %v, want the older instance's report", got["latency"])
	}
	// A lens no instance reports yields nil, not an empty map.
	if got := refractorLensOverlay(keys, read, "X", "unreported"); got != nil {
		t.Errorf("unreported lens overlay = %v, want nil", got)
	}
}

func TestBuildLensDetail(t *testing.T) {
	coreEnvs := map[string][]byte{
		"vtx.meta.L1":               []byte(`{"class":"meta.lens","data":{}}`),
		"vtx.meta.L1.canonicalName": specEnv(map[string]any{"value": "applicantRoster"}),
		"vtx.meta.L1.description":   specEnv(map[string]any{"value": "who applied"}),
		"vtx.meta.L1.spec": specEnv(map[string]any{
			"engine": "full", "cypherRule": "MATCH (a) RETURN a.key AS key",
			"targetType":   "nats_kv",
			"targetConfig": map[string]any{"bucket": "roster-bucket", "key": "key"},
		}),
		"vtx.package.P1.manifest": specEnv(map[string]any{
			"name": "loftspace", "version": "1.0.0", "declaredKeys": []any{"vtx.meta.L1"},
		}),
	}
	coreKeys := make([]string, 0, len(coreEnvs))
	for k := range coreEnvs {
		coreKeys = append(coreKeys, k)
	}
	coreGet := func(k string) ([]byte, bool) { v, ok := coreEnvs[k]; return v, ok }

	healthDocs := map[string]map[string]any{
		"L1": {"status": "active", "consumerLag": float64(0), "errorCount": float64(0),
			"activeSequence": float64(42), "lastUpdated": "2026-07-02T12:00:00Z"},
	}
	healthKeys := []string{"L1"}
	readEntry := func(k string) (map[string]any, bool) { d, ok := healthDocs[k]; return d, ok }

	detail, found := buildLensDetail("L1", healthKeys, readEntry, coreKeys, coreGet)
	if !found {
		t.Fatal("L1 not found")
	}
	if detail["canonicalName"] != "applicantRoster" || detail["status"] != "projecting" {
		t.Errorf("detail head = %v / %v", detail["canonicalName"], detail["status"])
	}
	rep := detail["reporter"].(map[string]any)
	if rep["found"] != true || rep["activeSequence"] != float64(42) || rep["freshness"] == nil {
		t.Errorf("reporter = %+v", rep)
	}
	def := detail["definition"].(map[string]any)
	if def["engine"] != "full" || def["targetType"] != "nats_kv" {
		t.Errorf("definition = %+v", def)
	}
	pkg := detail["package"].(*lensPackageRef)
	if pkg.Name != "loftspace" {
		t.Errorf("package = %+v", pkg)
	}

	// Meta-vertex without a reporter: still found, status unknown, reporter
	// found=false.
	coreEnvs["vtx.meta.L2"] = []byte(`{"class":"meta.lens"}`)
	detail, found = buildLensDetail("L2", healthKeys, readEntry, append(coreKeys, "vtx.meta.L2"), coreGet)
	if !found || detail["status"] != "unknown" {
		t.Errorf("reporterless lens = found %v, %+v", found, detail)
	}
	if rep := detail["reporter"].(map[string]any); rep["found"] != false {
		t.Errorf("reporterless reporter = %+v", rep)
	}

	// A tombstoned meta-vertex surfaces isDeleted so the page can flag a
	// stale bookmark instead of rendering a live-looking lens.
	coreEnvs["vtx.meta.L3"] = []byte(`{"class":"meta.lens","isDeleted":true}`)
	detail, found = buildLensDetail("L3", healthKeys, readEntry, append(coreKeys, "vtx.meta.L3"), coreGet)
	if !found || detail["isDeleted"] != true {
		t.Errorf("tombstoned lens = found %v, isDeleted %v", found, detail["isDeleted"])
	}
	if detail["isDeleted"] != true {
		t.Errorf("tombstoned meta not flagged: %+v", detail)
	}

	// Neither meta-vertex nor reporter: not found (404).
	if _, found := buildLensDetail("Ghost", healthKeys, readEntry, coreKeys, coreGet); found {
		t.Error("ghost lens reported found")
	}
}

func TestSelectLensRows(t *testing.T) {
	keys := []string{"row.c", "row.a", "row.b", "other.x"}
	sel, total, trunc := selectLensRows(keys, "", 10)
	if total != 4 || trunc || len(sel) != 4 || sel[0] != "other.x" || sel[1] != "row.a" {
		t.Errorf("unfiltered = %v total %d trunc %v", sel, total, trunc)
	}
	// Case-insensitive substring filter.
	sel, total, trunc = selectLensRows(keys, "ROW", 2)
	if total != 3 || !trunc || len(sel) != 2 || sel[0] != "row.a" {
		t.Errorf("filtered = %v total %d trunc %v", sel, total, trunc)
	}
	if sel, total, _ := selectLensRows(keys, "nomatch", 10); total != 0 || len(sel) != 0 {
		t.Errorf("nomatch = %v total %d", sel, total)
	}
}

// TestLensRowsTarget pins the CONTENTS panel's target decision: nats_kv is
// bucket-browsable, postgres routes to the read seam, and a blank or unknown
// targetType is an ERROR — a malformed spec must not masquerade as either
// browsable state.
func TestLensRowsTarget(t *testing.T) {
	kv := lensFullSpec{Found: true, TargetType: "nats_kv", Target: map[string]any{"bucket": "b1"}}
	if kind, bucket, _ := lensRowsTarget(kv); kind != rowsTargetKV || bucket != "b1" {
		t.Errorf("nats_kv = %q/%q", kind, bucket)
	}
	noBucket := lensFullSpec{Found: true, TargetType: "nats_kv", Target: map[string]any{}}
	if kind, _, msg := lensRowsTarget(noBucket); kind != rowsTargetBad || msg == "" {
		t.Errorf("bucketless kv = %q/%q", kind, msg)
	}
	pg := lensFullSpec{Found: true, TargetType: "postgres"}
	if kind, _, _ := lensRowsTarget(pg); kind != rowsTargetPG {
		t.Errorf("postgres = %q, want the pg seam", kind)
	}
	for _, tt := range []string{"", "mystery"} {
		spec := lensFullSpec{Found: true, TargetType: tt}
		if kind, _, msg := lensRowsTarget(spec); kind != rowsTargetBad || msg == "" {
			t.Errorf("targetType %q = %q/%q, want bad+message", tt, kind, msg)
		}
	}
}

// TestHandleLens_Routing pins the /api/lens/ dispatch shapes that decide
// before requireConn or after it with a bad id: method gate, path shapes, and
// the dot-free id rule (a dotted id would build a wildcard-missing control
// subject elsewhere; here it is rejected before any bucket work).
func TestHandleLens_Routing(t *testing.T) {
	mux := testServer()
	cases := []struct {
		name, method, path string
		want               int
	}{
		{"post rejected", http.MethodPost, "/api/lens/L1", http.StatusBadRequest},
		{"empty id", http.MethodGet, "/api/lens/", http.StatusBadRequest},
		{"extra segment", http.MethodGet, "/api/lens/L1/rows/extra", http.StatusBadRequest},
		{"wrong tail", http.MethodGet, "/api/lens/L1/cols", http.StatusBadRequest},
		// Well-shaped paths reach requireConn → 502 on the nil-conn harness.
		{"detail shape", http.MethodGet, "/api/lens/L1", http.StatusBadGateway},
		{"rows shape", http.MethodGet, "/api/lens/L1/rows?limit=5&q=x", http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.want {
				t.Errorf("%s %s = %d, want %d (body %s)", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
