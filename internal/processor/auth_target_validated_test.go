// Security proof for the validated-target primitive: `authContext.target` is a
// client-supplied field the Gateway forwards verbatim, so a guard that exempts a
// caller from confinement on its mere presence is forgeable by any scope=any
// holder. `op.authTargetValidated` is true only where step 3 actually checked
// the target, and these tests pin that mapping over every auth path.
package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestAuthTargetValidated_PathScopeMatrix walks every (Path, Scope) shape a
// step-3 decision can produce. The two true cases are the only two places the
// authorizer compares target against something it knows.
func TestAuthTargetValidated_PathScopeMatrix(t *testing.T) {
	cases := []struct {
		name string
		rp   *ResolvedPermission
		want bool
	}{
		{
			name: "platform scope=self — target proven == actor",
			rp:   &ResolvedPermission{Path: "platform", PlatformPermission: &PlatformPermission{Scope: "self"}},
			want: true,
		},
		{
			name: "task — target proven == the grant's target",
			rp:   &ResolvedPermission{Path: "task", EphemeralGrant: &EphemeralGrant{Target: capTestTargetKey}},
			want: true,
		},
		{
			name: "task with an EMPTY grant target — agreed about nothing, not validated",
			rp:   &ResolvedPermission{Path: "task", EphemeralGrant: &EphemeralGrant{Target: ""}},
			want: false,
		},
		{
			name: "task path with a nil grant — fail closed, never panic",
			rp:   &ResolvedPermission{Path: "task"},
			want: false,
		},
		{
			name: "platform scope=any — target never read",
			rp:   &ResolvedPermission{Path: "platform", PlatformPermission: &PlatformPermission{Scope: "any"}},
			want: false,
		},
		{
			name: "platform scope=specific — not implemented, never authorizes",
			rp:   &ResolvedPermission{Path: "platform", PlatformPermission: &PlatformPermission{Scope: "specific"}},
			want: false,
		},
		{
			name: "platform scope=owned — Phase 2, never authorizes",
			rp:   &ResolvedPermission{Path: "platform", PlatformPermission: &PlatformPermission{Scope: "owned"}},
			want: false,
		},
		{
			name: "service — matchServiceAccess never reads target",
			rp:   &ResolvedPermission{Path: "service", ServiceAccess: &ServiceAccessEntry{Service: capTestServiceKey}},
			want: false,
		},
		{
			name: "stub authorizer — makes no security claim",
			rp:   nil,
			want: false,
		},
		{
			name: "platform path with a nil permission — fail closed, never panic",
			rp:   &ResolvedPermission{Path: "platform"},
			want: false,
		},
		{
			name: "unrecognized path — fail closed",
			rp:   &ResolvedPermission{Path: "somethingNew"},
			want: false,
		},
		{
			name: "empty path (denial-shaped) — fail closed",
			rp:   &ResolvedPermission{},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := authTargetValidated(tc.rp); got != tc.want {
				t.Fatalf("authTargetValidated = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAuthTargetValidated_FromRealAuthorizerDecisions drives the real
// CapabilityAuthorizer rather than hand-built ResolvedPermissions, so the
// primitive stays pinned to what step 3 actually resolves — not to a
// test-local restatement of it that could drift.
func TestAuthTargetValidated_FromRealAuthorizerDecisions(t *testing.T) {
	now := time.Now().UTC()

	cases := []struct {
		name    string
		opType  string
		ac      *AuthContext
		want    bool
		wantWhy string
	}{
		{
			name:    "scope=self with target == actor",
			opType:  "ClaimIdentity",
			ac:      &AuthContext{Target: capTestActorKey},
			want:    true,
			wantWhy: "scope=self denies unless target == actor, so an authorized self decision proves it",
		},
		{
			name:    "scope=any with a forged target",
			opType:  "PingPlatform",
			ac:      &AuthContext{Target: "vtx.lease.someoneELsesLeaseKey"},
			want:    false,
			wantWhy: "scope=any authorizes without ever inspecting target — the forgery this primitive exists to defeat",
		},
		{
			name:    "scope=any with no authContext at all",
			opType:  "PingPlatform",
			ac:      nil,
			want:    false,
			wantWhy: "no target to validate",
		},
		{
			name:    "task grant whose target matches authContext.target",
			opType:  "ApproveLeaseApplication",
			ac:      &AuthContext{Task: capTestTaskKey, Target: capTestTargetKey},
			want:    true,
			wantWhy: "matchEphemeralGrant skips any grant whose target differs",
		},
		{
			name:    "service path",
			opType:  "BookExecutiveCleaning",
			ac:      &AuthContext{Service: capTestServiceKey, Target: "vtx.lease.aForgedTarget"},
			want:    false,
			wantWhy: "matchServiceAccess never reads target",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
			env := envFor(tc.opType, capTestActorKey, tc.ac)
			dec, err := a.Authorize(context.Background(), env)
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			// The negative vectors must be authorized-but-unvalidated, not
			// denied — a denial would make the assertion pass for the wrong
			// reason and prove nothing about the primitive.
			if !dec.Authorized {
				t.Fatalf("expected an AUTHORIZED decision to assert against; got %+v", dec)
			}
			if got := authTargetValidated(dec.Resolved); got != tc.want {
				t.Fatalf("authTargetValidated = %v, want %v (%s); resolved=%+v",
					got, tc.want, tc.wantWhy, dec.Resolved)
			}
		})
	}
}

// TestAuthTargetValidated_EmptyGrantTargetIsNotValidated drives the real
// authorizer over the one shape that authorizes on the task path while
// comparing nothing: a projected grant whose target is empty (an
// `OPTIONAL MATCH` on `scopedTo` that missed, unmarshalled as "") against an
// authContext carrying no target. `g.Target != ac.Target` is "" != "" — false —
// so the grant MATCHES, and a naive "task ⇒ validated" would hand this caller a
// confinement exemption they could not have had before the bit existed.
func TestAuthTargetValidated_EmptyGrantTargetIsNotValidated(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	doc.EphemeralGrants = append(doc.EphemeralGrants, EphemeralGrant{
		Source:        "task",
		TaskKey:       capTestTaskKey,
		OperationType: "UnscopedTaskOp",
		Target:        "", // the scopedTo link was absent or tombstoned at projection
		ExpiresAt:     now.Add(1 * time.Hour).Format(time.RFC3339Nano),
	})
	a, _, _ := newCapAuthForTest(t, doc, now)

	env := envFor("UnscopedTaskOp", capTestActorKey, &AuthContext{Task: capTestTaskKey})
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	// The match itself is expected — this test is about the derived bit, not
	// about whether an unscoped grant should authorize.
	if !dec.Authorized || dec.Resolved == nil || dec.Resolved.Path != "task" {
		t.Fatalf("expected the empty-target grant to match on the task path "+
			"(that is the premise of this vector); got authorized=%v resolved=%+v", dec.Authorized, dec.Resolved)
	}
	if authTargetValidated(dec.Resolved) {
		t.Fatal("a grant that named no target was treated as a VALIDATED target — " +
			"this exempts a caller who supplied no target at all, weaker than the " +
			"presence test the guards used before this bit existed")
	}
}

// TestAuthTargetValidated_NotAcceptedFromTheWire is the unforgeability proof:
// the field carries `json:"-"`, so a client asserting it in the envelope JSON
// cannot land it.
func TestAuthTargetValidated_NotAcceptedFromTheWire(t *testing.T) {
	raw := []byte(`{
		"requestId": "` + capTestActorID + `",
		"lane": "default",
		"operationType": "PingPlatform",
		"actor": "` + capTestActorKey + `",
		"submittedAt": "2026-05-15T10:00:00Z",
		"authTargetValidated": true,
		"authContext": {"target": "vtx.lease.aForgedTarget"},
		"payload": {}
	}`)
	env, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.AuthTargetValidated {
		t.Fatal("a client-supplied authTargetValidated landed on the envelope; the field must be wire-immune")
	}
	// And it never leaves on the wire either, so it cannot be echoed back as
	// though the platform had asserted it.
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if bytes.Contains(out, []byte("authTargetValidated")) {
		t.Fatalf("authTargetValidated serialized into the envelope JSON: %s", out)
	}
}

// TestAuthTargetValidated_ExposedToStarlark pins the script-visible field name
// and value — the guards in packages/* key their confinement exemption on it.
func TestAuthTargetValidated_ExposedToStarlark(t *testing.T) {
	for _, validated := range []bool{true, false} {
		env := &OperationEnvelope{
			RequestID:           capTestActorID,
			Lane:                LaneDefault,
			OperationType:       "PingPlatform",
			Actor:               capTestActorKey,
			SubmittedAt:         "2026-05-15T10:00:00Z",
			Payload:             json.RawMessage(`{}`),
			AuthContext:         &AuthContext{Target: capTestTargetKey},
			AuthTargetValidated: validated,
		}
		st := operationEnvelopeToStarlark(env)
		v, err := st.Attr("authTargetValidated")
		if err != nil {
			t.Fatalf("op.authTargetValidated not exposed to Starlark: %v", err)
		}
		if got := bool(v.Truth()); got != validated {
			t.Fatalf("op.authTargetValidated = %v, want %v", got, validated)
		}
		// The raw target stays visible alongside it — idiom-B ownership probes
		// and cafe's branch selector read it, and blanking it is what falsified
		// the earlier platform-wide approach.
		tv, err := st.Attr("authContextTarget")
		if err != nil || tv.String() != `"`+capTestTargetKey+`"` {
			t.Fatalf("authContextTarget must stay readable; got %v (err=%v)", tv, err)
		}
	}
}
