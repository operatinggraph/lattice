package pkgmgr

import (
	"strings"
	"testing"
)

// TestValidateLensBuckets_RejectsCapabilityAlias asserts a package lens
// declaring the short alias "capability" is rejected with a clear error that
// names the canonical bucket the author must use instead.
func TestValidateLensBuckets_RejectsCapabilityAlias(t *testing.T) {
	def := Definition{
		Name:    "rbac-domain",
		Version: "0.1.0",
		Lenses: []LensSpec{
			{
				CanonicalName: "capabilityRoles",
				Class:         "meta.lens",
				Adapter:       "nats-kv",
				Bucket:        "capability",
				Engine:        "full",
				Spec:          `MATCH (i:identity) RETURN i.key AS key`,
			},
		},
	}

	err := def.validateLensBuckets()
	if err == nil {
		t.Fatal("expected lens declaring Bucket \"capability\" to be rejected, got nil error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "capabilityRoles") {
		t.Errorf("error should name the offending lens; got %q", msg)
	}
	if !strings.Contains(msg, "capability-kv") {
		t.Errorf("error should direct the author to the canonical bucket %q; got %q", "capability-kv", msg)
	}
}

// TestValidateLensBuckets_AcceptsCanonicalBucket asserts the now-correct
// rbac-domain shape (Bucket "capability-kv") and an ordinary package bucket
// both pass validation.
func TestValidateLensBuckets_AcceptsCanonicalBucket(t *testing.T) {
	def := Definition{
		Name:    "rbac-domain",
		Version: "0.1.0",
		Lenses: []LensSpec{
			{
				CanonicalName: "capabilityRoles",
				Bucket:        "capability-kv",
			},
			{
				CanonicalName: "duplicateCandidates",
				Bucket:        "leads-kv",
			},
		},
	}

	if err := def.validateLensBuckets(); err != nil {
		t.Fatalf("expected canonical buckets to pass validation, got: %v", err)
	}
}

func TestValidateLensAdapters_NatsKV_RequiresBucket(t *testing.T) {
	def := Definition{
		Name:    "pkg",
		Version: "0.1.0",
		Lenses:  []LensSpec{{CanonicalName: "L", Adapter: "nats-kv"}},
	}
	if err := def.validateLensAdapters(); err == nil {
		t.Fatal("expected error for nats-kv lens missing Bucket, got nil")
	}
}

func TestValidateLensAdapters_EmptyAdapter_RequiresBucket(t *testing.T) {
	def := Definition{
		Name:    "pkg",
		Version: "0.1.0",
		Lenses:  []LensSpec{{CanonicalName: "L", Adapter: ""}},
	}
	if err := def.validateLensAdapters(); err == nil {
		t.Fatal("expected error for default-adapter lens missing Bucket, got nil")
	}
}

func TestValidateLensAdapters_Postgres_RequiresTable(t *testing.T) {
	cases := []struct {
		name string
		lens LensSpec
	}{
		{"missing both", LensSpec{CanonicalName: "L", Adapter: "postgres"}},
		{"missing Table", LensSpec{CanonicalName: "L", Adapter: "postgres", DSN: "postgres://x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := Definition{Name: "pkg", Version: "0.1.0", Lenses: []LensSpec{tc.lens}}
			if err := def.validateLensAdapters(); err == nil {
				t.Fatalf("expected validation error for %s, got nil", tc.name)
			}
		})
	}
}

// A postgres lens may omit DSN — Refractor resolves it from REFRACTOR_PG_DSN at
// activation, so a package declares posture + columns, not a connection string.
func TestValidateLensAdapters_Postgres_EmptyDSN_Allowed(t *testing.T) {
	def := Definition{
		Name:    "pkg",
		Version: "0.1.0",
		Lenses:  []LensSpec{{CanonicalName: "L", Adapter: "postgres", Table: "t"}},
	}
	if err := def.validateLensAdapters(); err != nil {
		t.Fatalf("expected empty-DSN postgres lens to pass (env-resolved at activation), got: %v", err)
	}
}

// A GrantTable lens defaults its Table to actor_read_grants, so it needs neither
// Table nor DSN declared.
func TestValidateLensAdapters_Postgres_GrantTable_TableOptional(t *testing.T) {
	def := Definition{
		Name:    "pkg",
		Version: "0.1.0",
		Lenses:  []LensSpec{{CanonicalName: "L", Adapter: "postgres", GrantTable: true}},
	}
	if err := def.validateLensAdapters(); err != nil {
		t.Fatalf("expected grant-table lens with no Table to pass, got: %v", err)
	}
}

func TestValidateLensAdapters_Postgres_Valid(t *testing.T) {
	def := Definition{
		Name:    "pkg",
		Version: "0.1.0",
		Lenses: []LensSpec{{
			CanonicalName: "L",
			Adapter:       "postgres",
			DSN:           "postgres://localhost/mydb",
			Table:         "projection_table",
		}},
	}
	if err := def.validateLensAdapters(); err != nil {
		t.Fatalf("expected valid postgres lens to pass, got: %v", err)
	}
}

func TestValidateLensAdapters_UnknownAdapter_Rejected(t *testing.T) {
	def := Definition{
		Name:    "pkg",
		Version: "0.1.0",
		Lenses:  []LensSpec{{CanonicalName: "L", Adapter: "redis"}},
	}
	err := def.validateLensAdapters()
	if err == nil {
		t.Fatal("expected error for unknown adapter, got nil")
	}
	if !strings.Contains(err.Error(), "redis") {
		t.Errorf("error should name the bad adapter value; got %q", err)
	}
}

func TestValidateLensAdapters_NatsKV_Valid(t *testing.T) {
	def := Definition{
		Name:    "pkg",
		Version: "0.1.0",
		Lenses: []LensSpec{{
			CanonicalName: "L",
			Adapter:       "nats-kv",
			Bucket:        "my-bucket",
		}},
	}
	if err := def.validateLensAdapters(); err != nil {
		t.Fatalf("expected valid nats-kv lens to pass, got: %v", err)
	}
}
