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
