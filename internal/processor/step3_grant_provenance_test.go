package processor

import (
	"context"
	"testing"
	"time"
)

// The reserved-operationType refusal (Contract #6 §6.1 rule 3,
// grant-provenance-runtime-permission-minting-design.md Move 3).
//
// The mechanism is an AUDIT distinction, not a deny-list: the same verb is
// allowed through one authoring channel and refused through the other. That is
// what these tests exist to hold — a deny-list implementation would pass every
// refusal assertion below and fail the package-origin ones, and a no-op
// implementation would pass every allow assertion and fail the refusals.

// provenanceDoc builds a fresh actor doc whose platformPermissions are exactly
// the entries given, so a case's outcome can't ride on freshDoc's unrelated
// baseline grants.
func provenanceDoc(t *testing.T, perms ...PlatformPermission) (*CapabilityAuthorizer, *recordingEmitter) {
	t.Helper()
	now := time.Now().UTC()
	doc := freshDoc(now)
	doc.PlatformPermissions = perms
	a, emitter, _ := newCapAuthForTest(t, doc, now)
	return a, emitter
}

// reservedOp is the reserved set's destruction verb; reservedUpdateOp is its
// write-once guard. Both are asserted present below rather than assumed, so
// narrowing the set can never silently un-test one of these.
const (
	reservedOp       = "ShredRetentionClassKey"
	reservedUpdateOp = "UpdatePermission"
)

func TestReservedOperationTypes_V1Set(t *testing.T) {
	if !reservedOperationTypes[reservedOp] {
		t.Fatalf("%s must be in the reserved set; got %v", reservedOp, reservedOperationTypes)
	}
	// UpdatePermission is the design's own write-once precondition. Inc 1
	// withdrew rbac-domain's grant of it, which closes the DECLARED channel —
	// but CreatePermission takes operationType as a free string, so an actor
	// can mint a brand-new UpdatePermission vertex at runtime and grant it to a
	// role they hold. Holding it means being able to rewrite any permission
	// vertex's body, including stripping the origin stamp off a package's
	// reserved grant and silently downgrading it to unstamped→runtime→refused.
	if !reservedOperationTypes[reservedUpdateOp] {
		t.Fatalf("%s must be reserved — withdrawing its package grant closes only the "+
			"declared channel; a runtime self-mint reopens the body-rewrite hole the "+
			"write-once invariant depends on", reservedUpdateOp)
	}
	// The kernel's own seeded grants (internal/bootstrap/primordial.go) carry
	// no origin and therefore read as `runtime` here. Reserving one of them
	// would lock the operator out of package installation itself, so the set
	// must stay clear of them until those seeds are stamped.
	for _, kernelSeeded := range []string{
		"CreateMetaVertex", "UpdateMetaVertex", "TombstoneMetaVertex",
		"InstallPackage", "UninstallPackage", "UpgradePackage",
	} {
		if reservedOperationTypes[kernelSeeded] {
			t.Errorf("%s is granted by an UNSTAMPED kernel-seeded permission vertex "+
				"(internal/bootstrap/primordial.go), which reads as runtime origin — reserving it "+
				"denies the operator the kernel's own verbs. Stamp the seed first.", kernelSeeded)
		}
	}
}

// TestMatchPlatformPermission_ReservedOpByOrigin is the §5.1 matrix: two
// origins × two operationTypes. The two package-origin rows are the positive
// vectors (standing checklist #3) — without them a fixture that denied
// everything would satisfy both refusal rows.
func TestMatchPlatformPermission_ReservedOpByOrigin(t *testing.T) {
	cases := []struct {
		name       string
		origin     string
		opType     string
		wantAllow  bool
		wantAlert  bool
		wantReason string
	}{
		{
			name:      "package origin, reserved op — the sanctioned deployment decision",
			origin:    "package",
			opType:    reservedOp,
			wantAllow: true,
		},
		{
			name:      "package origin, ordinary op — unaffected",
			origin:    "package",
			opType:    "PingPlatform",
			wantAllow: true,
		},
		{
			name:      "runtime origin, ordinary op — a self-mint of an unreserved verb still works",
			origin:    "runtime",
			opType:    "PingPlatform",
			wantAllow: true,
		},
		{
			name:       "runtime origin, reserved op — refused + alerted",
			origin:     "runtime",
			opType:     reservedOp,
			wantAllow:  false,
			wantAlert:  true,
			wantReason: "runtime-authored grant may not confer reserved operationType " + reservedOp,
		},
		{
			// Absence is the migration's whole safety property: every vertex
			// minted before provenance stamping existed carries no stamp,
			// and each must be governed rather than exempted.
			name:       "ABSENT origin, reserved op — reads as runtime, refused",
			origin:     "",
			opType:     reservedOp,
			wantAllow:  false,
			wantAlert:  true,
			wantReason: "runtime-authored grant may not confer reserved operationType " + reservedOp,
		},
		{
			name:      "absent origin, ordinary op — an unmigrated vertex keeps working",
			origin:    "",
			opType:    "PingPlatform",
			wantAllow: true,
		},
		{
			// The write-once bypass: mint UpdatePermission as a NEW permission
			// vertex (CreatePermission's operationType is a free string, so
			// Inc 1's grant withdrawal does not stop this) and grant it.
			name:       "runtime origin, UpdatePermission — the body-rewrite bypass, refused",
			origin:     "runtime",
			opType:     reservedUpdateOp,
			wantAllow:  false,
			wantAlert:  true,
			wantReason: "runtime-authored grant may not confer reserved operationType " + reservedUpdateOp,
		},
		{
			name:       "absent origin, UpdatePermission — reads as runtime, refused",
			origin:     "",
			opType:     reservedUpdateOp,
			wantAllow:  false,
			wantAlert:  true,
			wantReason: "runtime-authored grant may not confer reserved operationType " + reservedUpdateOp,
		},
		{
			// A package MAY still declare it — the reservation constrains the
			// runtime channel only. rbac-domain deliberately does not (Inc 1,
			// asserted in packages/rbac-domain/grant_withdrawal_test.go), but
			// that is a package's choice, not a platform prohibition.
			name:      "package origin, UpdatePermission — a declared grant still authorizes",
			origin:    "package",
			opType:    reservedUpdateOp,
			wantAllow: true,
		},
		{
			// Not "empty or runtime" but "anything that is not package": an
			// unrecognized value must fail closed, or a typo in a future lens
			// becomes an exemption.
			name:       "UNKNOWN origin, reserved op — fails closed like runtime",
			origin:     "kernel-seeded-someday",
			opType:     reservedOp,
			wantAllow:  false,
			wantAlert:  true,
			wantReason: "runtime-authored grant may not confer reserved operationType " + reservedOp,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, emitter := provenanceDoc(t,
				PlatformPermission{OperationType: tc.opType, Scope: "any", Origin: tc.origin})

			dec, err := a.Authorize(context.Background(), envFor(tc.opType, capTestActorKey, nil))
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if dec.Authorized != tc.wantAllow {
				t.Fatalf("Authorized = %v, want %v (%+v)", dec.Authorized, tc.wantAllow, dec)
			}
			if !tc.wantAllow {
				if dec.Code != ErrCodeAuthDenied {
					t.Errorf("Code = %q, want %q", dec.Code, ErrCodeAuthDenied)
				}
				if dec.Reason != tc.wantReason {
					t.Errorf("Reason = %q, want %q", dec.Reason, tc.wantReason)
				}
			}

			if !tc.wantAlert {
				for _, c := range emitter.calls {
					if c.code == AlertCodeReservedOperationGrantRejected {
						t.Fatalf("no reserved-operation alert expected here; got %+v", emitter.calls)
					}
				}
				return
			}
			var alert *struct {
				code    string
				details map[string]any
			}
			for i := range emitter.calls {
				if emitter.calls[i].code == AlertCodeReservedOperationGrantRejected {
					alert = &emitter.calls[i]
				}
			}
			if alert == nil {
				t.Fatalf("expected a %s alert; got %+v",
					AlertCodeReservedOperationGrantRejected, emitter.calls)
			}
			if got := alert.details["operationType"]; got != tc.opType {
				t.Errorf("alert details.operationType = %v, want %v", got, tc.opType)
			}
			if got := alert.details["actor"]; got != capTestActorKey {
				t.Errorf("alert details.actor = %v, want %v", got, capTestActorKey)
			}
			if got := alert.details["requestId"]; got != capTestActorID {
				t.Errorf("alert details.requestId = %v, want %v", got, capTestActorID)
			}
			// The observed origin, verbatim — an operator reading the alert
			// needs to know whether they are looking at a self-mint or an
			// unmigrated vertex, and those differ only by this field.
			if got := alert.details["origin"]; got != tc.origin {
				t.Errorf("alert details.origin = %v, want %q", got, tc.origin)
			}
		})
	}
}

// TestMatchPlatformPermission_ReservedOp_PackageEntryStillWinsAfterRuntimeRefusal
// is the case a first-match or return-on-refusal implementation gets wrong. One
// actor legitimately holds the same operationType from more than one role —
// that is the documented reason matchPlatformPermission scans rather than
// decides on the first row — so a refusal must retire ONE ENTRY, not the
// operation. The runtime entry is placed FIRST so the refusal is what the scan
// meets first; ordering here is the projection's, i.e. the order the actor's
// holdsRole edges happened to be written, which must never decide authority.
func TestMatchPlatformPermission_ReservedOp_PackageEntryStillWinsAfterRuntimeRefusal(t *testing.T) {
	a, emitter := provenanceDoc(t,
		PlatformPermission{OperationType: reservedOp, Scope: "any", Origin: "runtime"},
		PlatformPermission{OperationType: reservedOp, Scope: "any", Origin: "package"},
	)

	dec, err := a.Authorize(context.Background(), envFor(reservedOp, capTestActorKey, nil))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("the package-declared entry must still authorize after the runtime entry is "+
			"refused — a refusal retires an entry, not the operation; got %+v", dec)
	}

	// The refusal still happened and is still visible: the operator must learn
	// that someone minted themselves this grant even though the op proceeded on
	// other authority.
	found := false
	for _, c := range emitter.calls {
		if c.code == AlertCodeReservedOperationGrantRejected {
			found = true
		}
	}
	if !found {
		t.Fatalf("the runtime entry's refusal must alert even when a package entry authorizes; got %+v",
			emitter.calls)
	}
}

// Reverse order of the above. Same outcome required: if the package entry came
// first the scan returns before ever reading the runtime one, so this half
// proves the allow is not an accident of the refusal running first.
func TestMatchPlatformPermission_ReservedOp_PackageEntryFirstAuthorizes(t *testing.T) {
	a, _ := provenanceDoc(t,
		PlatformPermission{OperationType: reservedOp, Scope: "any", Origin: "package"},
		PlatformPermission{OperationType: reservedOp, Scope: "any", Origin: "runtime"},
	)
	dec, err := a.Authorize(context.Background(), envFor(reservedOp, capTestActorKey, nil))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("expected the package entry to authorize; got %+v", dec)
	}
}

// The reservation is on the operationType under runtime origin, at ANY scope.
// Nesting the check inside a scope arm would leave the scopes it missed as
// open channels to the same verb.
func TestMatchPlatformPermission_ReservedOp_RefusedAtEveryScope(t *testing.T) {
	for _, scope := range []string{"any", "self", "specific", "owned", "somethingUnknown"} {
		t.Run(scope, func(t *testing.T) {
			a, emitter := provenanceDoc(t,
				PlatformPermission{OperationType: reservedOp, Scope: scope, Origin: "runtime"})

			// scope=self would otherwise authorize with target == actor; give
			// it exactly that, so the only thing standing between the actor
			// and the verb is the provenance gate.
			dec, err := a.Authorize(context.Background(),
				envFor(reservedOp, capTestActorKey, &AuthContext{Target: capTestActorKey}))
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if dec.Authorized {
				t.Fatalf("scope=%s: a runtime-origin reserved grant must be refused at every scope; got %+v",
					scope, dec)
			}
			if dec.Reason != "runtime-authored grant may not confer reserved operationType "+reservedOp {
				t.Fatalf("scope=%s: refusal must come from the provenance gate, not the scope switch; got %q",
					scope, dec.Reason)
			}
			if len(emitter.calls) == 0 {
				t.Fatalf("scope=%s: expected an alert", scope)
			}
		})
	}

	// The positive control for the above: at scope=self the SAME shape with a
	// package origin authorizes, so the refusals above are the provenance gate
	// and not scope=self being broken.
	a, _ := provenanceDoc(t,
		PlatformPermission{OperationType: reservedOp, Scope: "self", Origin: "package"})
	dec, err := a.Authorize(context.Background(),
		envFor(reservedOp, capTestActorKey, &AuthContext{Target: capTestActorKey}))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("control: a package-origin scope=self grant of the reserved op must authorize; got %+v", dec)
	}
}

// --- §10's privileged-lane obligation --------------------------------------
//
// The design (§10) requires that a runtime-origin entry cannot silently reach a
// privileged lane. Today it structurally cannot: neither CreatePermission nor
// UpdatePermission takes a `lanes` parameter, so no runtime mint carries Lanes
// at all. That containment is a property of a DDL's parameter list, not a
// mechanism — which is exactly why it must be proven at the mechanism instead
// of relied on upstream. These construct the struct directly (legitimate: the
// struct has no origin-conditioned path, and the point is precisely to test the
// shape the DDL cannot currently produce) and assert platformLaneGate's
// allowlist fires unconditional of origin, so that adding a `lanes` parameter
// later cannot open a hole without failing a test.

func TestMatchPlatformPermission_RuntimeOriginCannotClaimPrivilegedLane(t *testing.T) {
	// Positive vector first: the allowlisted pair, at package origin, is
	// honored — so the refusal below is the allowlist and not a fixture that
	// denies every privileged lane.
	a, _ := provenanceDoc(t,
		PlatformPermission{OperationType: "InstallPackage", Scope: "any",
			Lanes: []string{"meta"}, Origin: "package"})
	dec, err := a.Authorize(context.Background(),
		envForLane("InstallPackage", capTestActorKey, LaneMeta, nil))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("control: InstallPackage@meta is allowlisted and must be honored; got %+v", dec)
	}

	// The same allowlisted pair at RUNTIME origin is equally honored — the
	// lane allowlist is a policy about {operationType, lane}, and this design
	// deliberately did not add an origin condition to it (a second, redundant
	// gate would drift out of step with the first).
	a, _ = provenanceDoc(t,
		PlatformPermission{OperationType: "InstallPackage", Scope: "any",
			Lanes: []string{"meta"}, Origin: "runtime"})
	dec, err = a.Authorize(context.Background(),
		envForLane("InstallPackage", capTestActorKey, LaneMeta, nil))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("an allowlisted {op, lane} pair must be honored regardless of origin; got %+v", dec)
	}

	// And an UNLISTED privileged lane is stripped at runtime origin exactly as
	// it is at package origin: this is §10's obligation, that a future `lanes`
	// parameter on CreatePermission cannot silently confer a privileged lane.
	a, emitter := provenanceDoc(t,
		PlatformPermission{OperationType: "TombstoneMetaVertex", Scope: "any",
			Lanes: []string{"meta"}, Origin: "runtime"})
	dec, _ = a.Authorize(context.Background(),
		envForLane("TombstoneMetaVertex", capTestActorKey, LaneMeta, nil))
	if dec.Authorized || dec.Code != ErrCodeLaneUnauthorized {
		t.Fatalf("a runtime-origin entry must not reach a non-allowlisted privileged lane; got %+v", dec)
	}
	if len(emitter.calls) != 1 || emitter.calls[0].code != AlertCodePrivilegedLaneGrantRejected {
		t.Fatalf("expected exactly one privileged-lane-grant-rejected alert; got %+v", emitter.calls)
	}
}

// A reserved op is refused on provenance BEFORE the lane gate ever runs, so a
// runtime-minted reserved grant cannot even produce a lane-shaped diagnostic
// that an attacker could use to probe the allowlist.
func TestMatchPlatformPermission_ReservedOpRefusedBeforeLaneGate(t *testing.T) {
	a, emitter := provenanceDoc(t,
		PlatformPermission{OperationType: reservedOp, Scope: "any",
			Lanes: []string{"meta"}, Origin: "runtime"})
	dec, _ := a.Authorize(context.Background(),
		envForLane(reservedOp, capTestActorKey, LaneMeta, nil))
	if dec.Authorized {
		t.Fatalf("expected refusal; got %+v", dec)
	}
	if dec.Code != ErrCodeAuthDenied {
		t.Fatalf("the provenance refusal must precede the lane gate; got code %q (%+v)", dec.Code, dec)
	}
	for _, c := range emitter.calls {
		if c.code == AlertCodePrivilegedLaneGrantRejected {
			t.Fatalf("the entry should never have reached the lane gate; got %+v", emitter.calls)
		}
	}
}

// --- alert emission is capped per Authorize call ---------------------------
//
// EmitAlert is a BLOCKING Health-KV write on the auth hot path (NFR-P3 = 500ms
// p99), and CreatePermission is unbounded and repeatable. An actor who mints N
// permission vertices for one reserved op and grants them all to a role they
// hold would, without a cap, charge every later submission of that op N
// sequential KVPutWithTTL calls — a self-inflicted latency amplification an
// attacker controls the size of.
//
// The cap is on the SIGNAL only. Authorization semantics are unchanged: every
// matching entry is still scanned and still refused, which is what lets a
// package-origin entry later in the doc win.
func TestMatchPlatformPermission_ReservedOp_AlertsOncePerAuthorize(t *testing.T) {
	a, emitter := provenanceDoc(t,
		PlatformPermission{OperationType: reservedOp, Scope: "any", Origin: "runtime"},
		PlatformPermission{OperationType: reservedOp, Scope: "any", Origin: "runtime"},
		PlatformPermission{OperationType: reservedOp, Scope: "self", Origin: ""},
	)

	dec, err := a.Authorize(context.Background(), envFor(reservedOp, capTestActorKey, nil))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("expected refusal; got %+v", dec)
	}

	reserved := 0
	for _, c := range emitter.calls {
		if c.code == AlertCodeReservedOperationGrantRejected {
			reserved++
		}
	}
	if reserved != 1 {
		t.Fatalf("expected exactly 1 reserved-operation alert for 3 refused entries, got %d — "+
			"an uncapped emit lets an actor amplify auth latency by minting more permission "+
			"vertices for the same op", reserved)
	}
}

// The cap must not leak ACROSS calls: a second submission is a second
// operator-visible event and must alert again. A package-level or
// authorizer-level latch would silence every refusal after the first, which is
// the opposite failure — the reservation would go quiet exactly when an actor
// kept retrying.
func TestMatchPlatformPermission_ReservedOp_AlertsAgainOnNextAuthorize(t *testing.T) {
	a, emitter := provenanceDoc(t,
		PlatformPermission{OperationType: reservedOp, Scope: "any", Origin: "runtime"},
		PlatformPermission{OperationType: reservedOp, Scope: "any", Origin: "runtime"},
	)

	for i := 0; i < 3; i++ {
		if _, err := a.Authorize(context.Background(), envFor(reservedOp, capTestActorKey, nil)); err != nil {
			t.Fatalf("Authorize #%d: %v", i, err)
		}
	}

	reserved := 0
	for _, c := range emitter.calls {
		if c.code == AlertCodeReservedOperationGrantRejected {
			reserved++
		}
	}
	if reserved != 3 {
		t.Fatalf("expected 1 alert per Authorize call (3 calls, 2 refused entries each), got %d — "+
			"the cap is per-call, never a latch that mutes repeat attempts", reserved)
	}
}

// WouldRefuseReservedGrant is the exported predicate internal/pkgmgr's
// grant-artifact scope check consumes, so that "may I submit this op" and "may
// my holding of it justify granting it" cannot drift apart. Pinned here at the
// source of truth.
func TestWouldRefuseReservedGrant(t *testing.T) {
	cases := []struct {
		op, origin string
		want       bool
	}{
		{reservedOp, "package", false},
		{reservedOp, "runtime", true},
		{reservedOp, "", true},
		{reservedOp, "unrecognized", true},
		{reservedUpdateOp, "package", false},
		{reservedUpdateOp, "runtime", true},
		{reservedUpdateOp, "", true},
		{"PingPlatform", "runtime", false},
		{"PingPlatform", "", false},
		{"PingPlatform", "package", false},
	}
	for _, tc := range cases {
		if got := WouldRefuseReservedGrant(tc.op, tc.origin); got != tc.want {
			t.Errorf("WouldRefuseReservedGrant(%q, %q) = %v, want %v", tc.op, tc.origin, got, tc.want)
		}
	}
}
