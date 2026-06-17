package pkgmgr

import (
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
