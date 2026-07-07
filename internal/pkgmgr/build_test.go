package pkgmgr

import (
	"encoding/json"
	"testing"
)

func TestLensSpecBody_NatsKV(t *testing.T) {
	body := lensSpecBody("lens-id-1", LensSpec{
		CanonicalName: "myLens",
		Adapter:       "nats-kv",
		Bucket:        "my-bucket",
		Engine:        "full",
		Spec:          "MATCH (n) RETURN n.key AS key",
	})

	if got := body["targetType"]; got != "nats_kv" {
		t.Errorf("targetType: want nats_kv, got %q", got)
	}
	cfg, ok := body["targetConfig"].(map[string]any)
	if !ok {
		t.Fatalf("targetConfig: not a map")
	}
	if cfg["bucket"] != "my-bucket" {
		t.Errorf("targetConfig.bucket: want my-bucket, got %v", cfg["bucket"])
	}
	if _, hasKey := cfg["key"]; !hasKey {
		t.Error("targetConfig.key: missing")
	}
	if _, hasDSN := cfg["dsn"]; hasDSN {
		t.Error("targetConfig should not contain dsn for nats-kv")
	}
}

func TestLensSpecBody_NatsKV_EmptyAdapterDefaultsToNatsKV(t *testing.T) {
	body := lensSpecBody("lens-id-2", LensSpec{
		CanonicalName: "myLens",
		Adapter:       "",
		Bucket:        "my-bucket",
		Engine:        "full",
		Spec:          "MATCH (n) RETURN n.key AS key",
	})
	if got := body["targetType"]; got != "nats_kv" {
		t.Errorf("targetType: want nats_kv for empty Adapter, got %q", got)
	}
}

func TestLensSpecBody_Postgres(t *testing.T) {
	body := lensSpecBody("lens-id-3", LensSpec{
		CanonicalName: "myPgLens",
		Adapter:       "postgres",
		DSN:           "postgres://localhost/mydb",
		Table:         "my_projection",
		Engine:        "full",
		Spec:          "MATCH (n) RETURN n.key AS key",
		IntoKey:       []string{"key"},
	})

	if got := body["targetType"]; got != "postgres" {
		t.Errorf("targetType: want postgres, got %q", got)
	}
	cfg, ok := body["targetConfig"].(map[string]any)
	if !ok {
		t.Fatalf("targetConfig: not a map")
	}
	if cfg["dsn"] != "postgres://localhost/mydb" {
		t.Errorf("targetConfig.dsn: want postgres://localhost/mydb, got %v", cfg["dsn"])
	}
	if cfg["table"] != "my_projection" {
		t.Errorf("targetConfig.table: want my_projection, got %v", cfg["table"])
	}
	if _, hasBucket := cfg["bucket"]; hasBucket {
		t.Error("targetConfig should not contain bucket for postgres")
	}
	if _, hasTimeout := cfg["queryTimeout"]; hasTimeout {
		t.Error("queryTimeout should be absent when QueryTimeout is empty")
	}
}

func TestLensSpecBody_Postgres_WithQueryTimeout(t *testing.T) {
	body := lensSpecBody("lens-id-4", LensSpec{
		CanonicalName: "myPgLens",
		Adapter:       "postgres",
		DSN:           "postgres://localhost/mydb",
		Table:         "my_projection",
		Engine:        "full",
		Spec:          "MATCH (n) RETURN n.key AS key",
		QueryTimeout:  "10s",
	})
	cfg, ok := body["targetConfig"].(map[string]any)
	if !ok {
		t.Fatalf("targetConfig: not a map")
	}
	if cfg["queryTimeout"] != "10s" {
		t.Errorf("targetConfig.queryTimeout: want 10s, got %v", cfg["queryTimeout"])
	}
}

func TestLensSpecBody_IntoKey_DefaultsToKey(t *testing.T) {
	body := lensSpecBody("lens-id-5", LensSpec{
		CanonicalName: "myLens",
		Adapter:       "nats-kv",
		Bucket:        "bucket",
		Engine:        "full",
		Spec:          "MATCH (n) RETURN n.key AS key",
	})
	cfg := body["targetConfig"].(map[string]any)
	keys, ok := cfg["key"].([]string)
	if !ok {
		t.Fatalf("key: not []string, got %T", cfg["key"])
	}
	if len(keys) != 1 || keys[0] != "key" {
		t.Errorf("key: want [key], got %v", keys)
	}
}

// TestLensSpecBody_EventStream_EmitsSource pins the Chronicler Fire 2 seam:
// a LensSpec with Source set must emit spec["source"] (the aspect data
// Refractor's CoreKVSource unmarshals into lens.SourceConfig), and must
// leave cypherRule empty (an event lens has no Core-KV vertex to MATCH).
func TestLensSpecBody_EventStream_EmitsSource(t *testing.T) {
	body := lensSpecBody("lens-id-ev", LensSpec{
		CanonicalName: "loomFlowHistory",
		Adapter:       "nats-kv",
		Bucket:        "orchestration-history",
		IntoKey:       []string{"instance_id"},
		Source: &SourceConfig{
			Kind:     "eventStream",
			Subjects: []string{"events.loom.>"},
			Project: &EventProjection{
				Key: "payload.instanceId",
				Columns: map[string]ColumnMapping{
					"instance_id": {Path: "payload.instanceId"},
				},
			},
		},
	})
	if got := body["cypherRule"]; got != "" {
		t.Errorf("cypherRule: want empty for an eventStream lens, got %q", got)
	}
	src, ok := body["source"].(*SourceConfig)
	if !ok {
		t.Fatalf("source: not *SourceConfig, got %T", body["source"])
	}
	if src.Kind != "eventStream" {
		t.Errorf("source.kind: want eventStream, got %q", src.Kind)
	}
}

func TestLensSpecBody_NoSource_OmitsSourceKey(t *testing.T) {
	body := lensSpecBody("lens-id-nosrc", LensSpec{
		CanonicalName: "myLens",
		Adapter:       "nats-kv",
		Bucket:        "bucket",
		Engine:        "full",
		Spec:          "MATCH (n) RETURN n.key AS key",
	})
	if _, has := body["source"]; has {
		t.Error("source: must be absent when LensSpec.Source is nil (every existing lens byte-for-byte unchanged)")
	}
}

// TestColumnMapping_MarshalJSON_WireShape asserts each of the three shapes
// encodes to what internal/refractor/lens.ColumnMapping.UnmarshalJSON
// expects — the two ColumnMapping types are independent (pkgmgr cannot
// import internal/refractor/lens without a cycle) but must agree on JSON
// shape across that package boundary.
func TestColumnMapping_MarshalJSON_WireShape(t *testing.T) {
	cases := map[string]ColumnMapping{
		"bare path": {Path: "payload.instanceId"},
		"from/map": {From: "eventType", Map: map[string]string{
			"loom.patternStarted": "running", "loom.patternCompleted": "complete",
		}},
		"when/value": {When: []string{"loom.patternStarted", "loom.patternCompleted"}, Value: "timestamp"},
	}
	for name, cm := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(cm)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			// pkgmgr.ColumnMapping has no custom UnmarshalJSON (pkgmgr never
			// reads this back — it only ever marshals for the install-op
			// payload); internal/refractor/lens.ColumnMapping is the reader,
			// exercised by that package's own round-trip test
			// (TestColumnMapping_MarshalJSON_RoundTrip in eventsource_test.go)
			// and by the cross-package install-path test
			// (TestManager_LoomFlowHistoryLens_E2E). Here, just assert this
			// package's wire shape is what that reader's UnmarshalJSON expects.
			var wire any
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Fatalf("decode wire shape: %v", err)
			}
			switch {
			case cm.Path != "":
				if wire != cm.Path {
					t.Errorf("bare path: wire = %v, want %q", wire, cm.Path)
				}
			case cm.From != "" || len(cm.Map) > 0:
				obj, ok := wire.(map[string]any)
				if !ok || obj["from"] != cm.From {
					t.Errorf("from/map: wire = %v", wire)
				}
			default:
				obj, ok := wire.(map[string]any)
				if !ok || obj["value"] != cm.Value {
					t.Errorf("when/value: wire = %v", wire)
				}
			}
		})
	}
}

// TestColumnMapping_MarshalJSON_RejectsMixedShapes pins the mutual-
// exclusivity guard added alongside the Chronicler Fire 2 round-trip fix:
// a malformed literal (e.g. a copy-paste mistake authoring a package's
// Lenses()) must fail loudly, not silently keep only the first-matched
// shape and drop the rest.
func TestColumnMapping_MarshalJSON_RejectsMixedShapes(t *testing.T) {
	cases := map[string]ColumnMapping{
		"path + from/map":   {Path: "payload.instanceId", From: "eventType", Map: map[string]string{"a": "b"}},
		"path + when/value": {Path: "payload.instanceId", When: []string{"a"}, Value: "timestamp"},
		"from/map + when/value": {
			From: "eventType", Map: map[string]string{"a": "b"},
			When: []string{"a"}, Value: "timestamp",
		},
	}
	for name, cm := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := json.Marshal(cm); err == nil {
				t.Error("expected an error for a mixed-shape ColumnMapping, got nil")
			}
		})
	}
}

func TestLensSpecBody_Postgres_Protected(t *testing.T) {
	body := lensSpecBody("lens-id-p", LensSpec{
		CanonicalName: "leaseApplications",
		Adapter:       "postgres",
		Table:         "read_lease_applications",
		Engine:        "full",
		Spec:          "MATCH (n) RETURN n.key AS key",
		IntoKey:       []string{"key"},
		Protected:     true,
		Columns: []PostgresColumn{
			{Name: "status", Type: "text"},
			{Name: "submitted_at", Type: "bigint"},
		},
	})
	cfg := body["targetConfig"].(map[string]any)
	if cfg["protected"] != true {
		t.Errorf("targetConfig.protected: want true, got %v", cfg["protected"])
	}
	if _, has := cfg["public"]; has {
		t.Error("public should be absent when not set")
	}
	if _, has := cfg["grantTable"]; has {
		t.Error("grantTable should be absent when not set")
	}
	cols, ok := cfg["columns"].([]map[string]any)
	if !ok {
		t.Fatalf("columns: not []map[string]any, got %T", cfg["columns"])
	}
	if len(cols) != 2 || cols[0]["name"] != "status" || cols[0]["type"] != "text" ||
		cols[1]["name"] != "submitted_at" || cols[1]["type"] != "bigint" {
		t.Errorf("columns: unexpected shape %v", cols)
	}
	// An empty DSN is serialized verbatim; Refractor resolves it at activation.
	if cfg["dsn"] != "" {
		t.Errorf("targetConfig.dsn: want empty, got %v", cfg["dsn"])
	}
}

func TestLensSpecBody_Postgres_Public(t *testing.T) {
	body := lensSpecBody("lens-id-pub", LensSpec{
		CanonicalName: "publicListings",
		Adapter:       "postgres",
		Table:         "listings_view",
		Spec:          "MATCH (n) RETURN n.key AS key",
		Public:        true,
	})
	cfg := body["targetConfig"].(map[string]any)
	if cfg["public"] != true {
		t.Errorf("targetConfig.public: want true, got %v", cfg["public"])
	}
	if _, has := cfg["protected"]; has {
		t.Error("protected should be absent for a public lens")
	}
}

// A GrantTable lens with no declared key omits `key` so Refractor applies the
// platform grant composite (actor_id, anchor_id, grant_source); its table also
// defaults at activation, so it serializes an empty table.
func TestLensSpecBody_Postgres_GrantTable_OmitsKeyDefault(t *testing.T) {
	body := lensSpecBody("lens-id-g", LensSpec{
		CanonicalName: "cap-read.residence",
		Adapter:       "postgres",
		Spec:          "MATCH (n) RETURN n.actor_id AS actor_id",
		GrantTable:    true,
	})
	cfg := body["targetConfig"].(map[string]any)
	if cfg["grantTable"] != true {
		t.Errorf("targetConfig.grantTable: want true, got %v", cfg["grantTable"])
	}
	if _, has := cfg["key"]; has {
		t.Errorf("key should be omitted for a grant lens (platform defaults the composite), got %v", cfg["key"])
	}
}

// A GrantTable lens may still pin an explicit key.
func TestLensSpecBody_Postgres_GrantTable_ExplicitKeyKept(t *testing.T) {
	body := lensSpecBody("lens-id-g2", LensSpec{
		CanonicalName: "cap-read.residence",
		Adapter:       "postgres",
		Spec:          "MATCH (n) RETURN n.actor_id AS actor_id",
		GrantTable:    true,
		IntoKey:       []string{"actor_id", "anchor_id", "grant_source"},
	})
	cfg := body["targetConfig"].(map[string]any)
	keys, ok := cfg["key"].([]string)
	if !ok || len(keys) != 3 {
		t.Fatalf("key: want explicit 3-col key, got %v (%T)", cfg["key"], cfg["key"])
	}
}

// A Secure Lens's secureColumns reach the on-wire targetConfig in the shape
// Refractor's TargetPostgresConfig parses (Contract #3 §3.10).
func TestLensSpecBody_Postgres_SecureColumns(t *testing.T) {
	body := lensSpecBody("lens-id-s1", LensSpec{
		CanonicalName: "applicantRosterRead",
		Adapter:       "postgres",
		Table:         "read_loftspace_identities",
		Spec:          "MATCH (i:identity) RETURN i.key AS identity_id",
		Protected:     true,
		Columns:       []PostgresColumn{{Name: "name", Type: "text"}, {Name: "identity_key", Type: "text"}},
		SecureColumns: []SecureColumn{{Column: "name", IdentityKeyColumn: "identity_key", Field: "value"}},
	})
	cfg := body["targetConfig"].(map[string]any)
	secure, ok := cfg["secureColumns"].([]map[string]any)
	if !ok || len(secure) != 1 {
		t.Fatalf("secureColumns: want 1 entry, got %v (%T)", cfg["secureColumns"], cfg["secureColumns"])
	}
	if secure[0]["column"] != "name" || secure[0]["identityKeyColumn"] != "identity_key" || secure[0]["field"] != "value" {
		t.Fatalf("secureColumns entry mismatch: %v", secure[0])
	}
}

func TestValidateLensReadPath(t *testing.T) {
	cases := []struct {
		name    string
		lens    LensSpec
		wantErr bool
	}{
		{"protected postgres ok", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Protected: true}, false},
		{"public postgres ok", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Public: true}, false},
		{"grant postgres ok", LensSpec{CanonicalName: "L", Adapter: "postgres", GrantTable: true}, false},
		{"plain postgres (neither declared) rejected", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t"}, true},
		{"protected on nats-kv rejected", LensSpec{CanonicalName: "L", Adapter: "nats-kv", Bucket: "b", Protected: true}, true},
		{"grant on default adapter rejected", LensSpec{CanonicalName: "L", Adapter: "", Bucket: "b", GrantTable: true}, true},
		{"columns on nats-kv rejected", LensSpec{CanonicalName: "L", Adapter: "nats-kv", Bucket: "b", Columns: []PostgresColumn{{Name: "x", Type: "text"}}}, true},
		{"protected and public rejected", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Protected: true, Public: true}, true},
		{"protected and grant rejected", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Protected: true, GrantTable: true}, true},
		{"public and grant rejected", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Public: true, GrantTable: true}, true},
		{"secure protected ok", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Protected: true,
			Columns:       []PostgresColumn{{Name: "name", Type: "text"}, {Name: "identity_key", Type: "text"}},
			SecureColumns: []SecureColumn{{Column: "name", IdentityKeyColumn: "identity_key", Field: "value"}}}, false},
		{"secure identity-key via IntoKey ok", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Protected: true,
			IntoKey:       []string{"identity_key"},
			Columns:       []PostgresColumn{{Name: "name", Type: "text"}},
			SecureColumns: []SecureColumn{{Column: "name", IdentityKeyColumn: "identity_key"}}}, false},
		{"secure reserved column rejected", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Protected: true,
			Columns:       []PostgresColumn{{Name: "authz_anchors", Type: "text[]"}, {Name: "identity_key", Type: "text"}},
			SecureColumns: []SecureColumn{{Column: "authz_anchors", IdentityKeyColumn: "identity_key"}}}, true},
		{"secure key-column overlap rejected", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Protected: true,
			IntoKey:       []string{"name"},
			Columns:       []PostgresColumn{{Name: "name", Type: "text"}, {Name: "identity_key", Type: "text"}},
			SecureColumns: []SecureColumn{{Column: "name", IdentityKeyColumn: "identity_key"}}}, true},
		{"secure undeclared identityKeyColumn rejected", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Protected: true,
			Columns:       []PostgresColumn{{Name: "name", Type: "text"}},
			SecureColumns: []SecureColumn{{Column: "name", IdentityKeyColumn: "identity_key"}}}, true},
		{"secure without protected rejected", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t",
			Columns:       []PostgresColumn{{Name: "name", Type: "text"}},
			SecureColumns: []SecureColumn{{Column: "name", IdentityKeyColumn: "identity_key"}}}, true},
		{"secure on nats-kv rejected", LensSpec{CanonicalName: "L", Adapter: "nats-kv", Bucket: "b",
			SecureColumns: []SecureColumn{{Column: "name", IdentityKeyColumn: "identity_key"}}}, true},
		{"secure on actor-aggregate rejected", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Protected: true, ProjectionKind: "actorAggregate",
			Columns:       []PostgresColumn{{Name: "name", Type: "text"}},
			SecureColumns: []SecureColumn{{Column: "name", IdentityKeyColumn: "identity_key"}}}, true},
		{"secure undeclared column rejected", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Protected: true,
			Columns:       []PostgresColumn{{Name: "name", Type: "text"}},
			SecureColumns: []SecureColumn{{Column: "ssn", IdentityKeyColumn: "identity_key"}}}, true},
		{"secure missing identityKeyColumn rejected", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Protected: true,
			Columns:       []PostgresColumn{{Name: "name", Type: "text"}},
			SecureColumns: []SecureColumn{{Column: "name"}}}, true},
		{"secure duplicate column rejected", LensSpec{CanonicalName: "L", Adapter: "postgres", Table: "t", Protected: true,
			Columns:       []PostgresColumn{{Name: "name", Type: "text"}},
			SecureColumns: []SecureColumn{{Column: "name", IdentityKeyColumn: "identity_key"}, {Column: "name", IdentityKeyColumn: "identity_key"}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := Definition{Name: "pkg", Version: "0.1.0", Lenses: []LensSpec{tc.lens}}
			err := def.validateLensReadPath()
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
		})
	}
}

// minimalDDL returns a DDLSpec satisfying buildInstallBatch's self-description
// gate, with the given canonicalName/class/sensitivity.
func minimalDDL(name, class string, sensitive bool) DDLSpec {
	return DDLSpec{
		CanonicalName:    name,
		Class:            class,
		Sensitive:        sensitive,
		Description:      name + " ddl",
		Script:           "def execute(state, op):\n    fail(\"noop\")\n",
		InputSchema:      `{"type":"object"}`,
		OutputSchema:     `{"type":"object"}`,
		FieldDescription: map[string]string{name: "the " + name},
		Examples:         []ExampleSpec{{Name: name, Payload: map[string]any{}, ExpectedOutcome: "ok"}},
	}
}

// findOp returns the install mutation for the given key, or false.
func findOp(ops []installMutation, key string) (installMutation, bool) {
	for _, op := range ops {
		if op.Key == key {
			return op, true
		}
	}
	return installMutation{}, false
}

// TestBuildInstallBatch_SensitiveAspectEmittedOnlyWhenTrue pins Item A: a DDL
// with Sensitive:true emits a `.sensitive` aspect carrying data.value=true; a
// default (Sensitive:false) DDL emits NO `.sensitive` aspect (opt-in
// regression pin — the read side, ddl_cache, treats absent as non-sensitive).
func TestBuildInstallBatch_SensitiveAspectEmittedOnlyWhenTrue(t *testing.T) {
	def := Definition{
		Name:    "sensitive-test-pkg",
		Version: "0.0.1",
		DDLs: []DDLSpec{
			minimalDDL("plainType", "meta.ddl.vertexType", false),
			minimalDDL("secretType", "meta.ddl.aspectType", true),
		},
	}

	inst := &Installer{}
	pkgKey := PackageVertexPrefix + EntityNanoIDForTest(def.Name, "package")
	ddlIDs := []string{
		EntityNanoIDForTest(def.Name, "ddl:plainType"),
		EntityNanoIDForTest(def.Name, "ddl:secretType"),
	}
	ops, _, err := inst.buildInstallBatch(def, pkgKey, ddlIDs, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildInstallBatch: %v", err)
	}

	plainKey := metaVertexPrefix + ddlIDs[0]
	secretKey := metaVertexPrefix + ddlIDs[1]

	// Sensitive DDL: `.sensitive` aspect present with data.value == true.
	sOp, ok := findOp(ops, secretKey+".sensitive")
	if !ok {
		t.Fatalf("sensitive DDL: no %s aspect emitted", secretKey+".sensitive")
	}
	if got := sOp.Document["class"]; got != "sensitive" {
		t.Errorf("sensitive aspect class = %v, want \"sensitive\"", got)
	}
	data, _ := sOp.Document["data"].(map[string]any)
	if v, _ := data["value"].(bool); !v {
		t.Errorf("sensitive aspect data.value = %v, want true", data["value"])
	}

	// Non-sensitive DDL: NO `.sensitive` aspect (the opt-in regression pin).
	if _, ok := findOp(ops, plainKey+".sensitive"); ok {
		t.Errorf("non-sensitive DDL emitted a %s aspect; want none (opt-in)", plainKey+".sensitive")
	}
}

// TestBuildInstallBatch_EffectsAspectEmittedOnlyWhenDeclared pins the Fire-6
// catalog-materialization seam: an op-meta vertex whose operationType carries
// a DDL Effects declaration gets a sibling `.effects` aspect carrying those
// guards verbatim; an op-meta vertex for an operationType with no Effects
// entry emits no such aspect (opt-in, byte-identical to every install before
// this fire).
func TestBuildInstallBatch_EffectsAspectEmittedOnlyWhenDeclared(t *testing.T) {
	ddl := minimalDDL("leaseapp", "meta.ddl.vertexType", false)
	ddl.PermittedCommands = []string{"SignLease", "CreateLeaseApplication"}
	ddl.Effects = map[string][]json.RawMessage{
		"SignLease": {json.RawMessage(`{"present":"subject.signature.data.signedAt"}`)},
	}
	def := Definition{
		Name:    "effects-test-pkg",
		Version: "0.0.1",
		DDLs:    []DDLSpec{ddl},
		OpMetas: []OpMetaSpec{{OperationType: "SignLease"}, {OperationType: "CreateLeaseApplication"}},
	}

	inst := &Installer{}
	pkgKey := PackageVertexPrefix + EntityNanoIDForTest(def.Name, "package")
	ddlIDs := []string{EntityNanoIDForTest(def.Name, "ddl:leaseapp")}
	opMetaIDs := []string{
		EntityNanoIDForTest(def.Name, "opMeta:SignLease"),
		EntityNanoIDForTest(def.Name, "opMeta:CreateLeaseApplication"),
	}
	ops, _, err := inst.buildInstallBatch(def, pkgKey, ddlIDs, nil, nil, nil, nil, nil, opMetaIDs)
	if err != nil {
		t.Fatalf("buildInstallBatch: %v", err)
	}

	signKey := metaVertexPrefix + opMetaIDs[0]
	createKey := metaVertexPrefix + opMetaIDs[1]

	op, ok := findOp(ops, signKey+".effects")
	if !ok {
		t.Fatalf("SignLease op-meta: no %s aspect emitted", signKey+".effects")
	}
	if got := op.Document["class"]; got != "effects" {
		t.Errorf("effects aspect class = %v, want \"effects\"", got)
	}
	data, _ := op.Document["data"].(map[string]any)
	guards, _ := data["guards"].([]json.RawMessage)
	if len(guards) != 1 {
		t.Fatalf("effects aspect guards = %v, want exactly 1 entry", data["guards"])
	}

	if _, ok := findOp(ops, createKey+".effects"); ok {
		t.Errorf("CreateLeaseApplication declares no Effects; want no %s aspect", createKey+".effects")
	}
}
