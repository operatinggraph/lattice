package controlauth

import (
	"context"
	"testing"
)

// RR-5 (edge-lattice-full-design.md §8.1): the untrusted-Edge ops guard. With
// no JWT trust root resolved, LATTICE_CONTROL_REQUIRE_ACTOR_VERIFIER=true must
// turn the "not configured" return into a hard startup error rather than
// degrading to self-asserted identity. The no-keys path returns before conn is
// touched, so a nil conn is safe here.
func TestWireActorVerifierFromEnv_RequireWithoutKeys_Errors(t *testing.T) {
	t.Setenv("LATTICE_CONTROL_JWT_KEYS_DIR", "")
	t.Setenv("LATTICE_CONTROL_JWT_DEV_MODE", "")
	t.Setenv("LATTICE_CONTROL_JWT_DEV_KEY_PATH", "")
	t.Setenv("LATTICE_CONTROL_REQUIRE_ACTOR_VERIFIER", "true")

	v, err := WireActorVerifierFromEnv(context.Background(), nil, nil)
	if err == nil {
		t.Fatalf("require=true with no trust root must error, got verifier=%v", v)
	}
	if v != nil {
		t.Fatalf("errored bring-up must return a nil verifier, got %v", v)
	}
}

// Without the require flag, the same no-keys configuration preserves the Fire 1
// default: verification not configured, self-asserted-header behavior stands.
func TestWireActorVerifierFromEnv_NoRequireNoKeys_NotConfigured(t *testing.T) {
	t.Setenv("LATTICE_CONTROL_JWT_KEYS_DIR", "")
	t.Setenv("LATTICE_CONTROL_JWT_DEV_MODE", "")
	t.Setenv("LATTICE_CONTROL_JWT_DEV_KEY_PATH", "")
	t.Setenv("LATTICE_CONTROL_REQUIRE_ACTOR_VERIFIER", "")

	v, err := WireActorVerifierFromEnv(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("no keys + no require must return (nil, nil), got err %v", err)
	}
	if v != nil {
		t.Fatalf("no keys must leave verification unconfigured, got %v", v)
	}
}
