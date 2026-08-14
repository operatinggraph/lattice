package pkgmgr

import (
	"testing"
)

// The grant-proposal laundering channel (grant-provenance-runtime-permission-
// minting-design.md, Contract #6 §6.1 rule 3).
//
// The proposal path is a SECOND writer of permission vertices, reached without
// any package author being involved: an approved `grant` artifact becomes a
// Definition (DefinitionForCapabilityArtifact) which Installer.Apply feeds to
// build.go's permission mint — and that mint stamps `origin: "package"` on
// everything it applies, repo package and approved proposal alike.
//
// So the ONLY thing standing between a refused runtime grant and an authorized
// package-origin one is this scope check. If a runtime-origin entry for a
// reserved op counted as "held", an operator could:
//
//	CreatePermission + GrantPermission   → origin runtime, refused at step 3
//	propose a `grant` artifact, same op  → requesterHolds says yes (the bug)
//	approve + apply                      → re-minted with origin "package"
//	submit the op                        → now AUTHORIZED
//
// …which is the whole reservation, undone, with a `declaredBy` naming a
// package that exists in no manifest.

const laundryReservedOp = "ShredRetentionClassKey"

// TestRequesterHolds_ReservedOpRuntimeOrigin_DoesNotCountAsHeld is the unit
// statement of the invariant: an entry step 3 would refuse confers no standing
// to grant that same op to anyone else.
func TestRequesterHolds_ReservedOpRuntimeOrigin_DoesNotCountAsHeld(t *testing.T) {
	cases := []struct {
		name     string
		held     []HeldPermission
		wantHeld bool
	}{
		{
			// POSITIVE VECTOR FIRST: the declared channel still works, so the
			// refusals below are the provenance gate and not a check that
			// rejects everything.
			name:     "package origin, reserved op — the sanctioned holding",
			held:     []HeldPermission{{OperationType: laundryReservedOp, Scope: "any", Origin: "package"}},
			wantHeld: true,
		},
		{
			name:     "runtime origin, ordinary op — unaffected",
			held:     []HeldPermission{{OperationType: "RescheduleAppointment", Scope: "any", Origin: "runtime"}},
			wantHeld: true,
		},
		{
			name:     "absent origin, ordinary op — an unmigrated entry still counts",
			held:     []HeldPermission{{OperationType: "RescheduleAppointment", Scope: "any"}},
			wantHeld: true,
		},
		{
			name:     "runtime origin, reserved op — THE LAUNDERING VECTOR",
			held:     []HeldPermission{{OperationType: laundryReservedOp, Scope: "any", Origin: "runtime"}},
			wantHeld: false,
		},
		{
			// Absence reads as runtime here exactly as at step 3 — an entry
			// predating the stamp must not become a laundering channel.
			name:     "absent origin, reserved op — reads as runtime",
			held:     []HeldPermission{{OperationType: laundryReservedOp, Scope: "any"}},
			wantHeld: false,
		},
		{
			name:     "unknown origin, reserved op — fails closed",
			held:     []HeldPermission{{OperationType: laundryReservedOp, Scope: "any", Origin: "whatever"}},
			wantHeld: false,
		},
		{
			name:     "UpdatePermission at runtime origin — the write-once bypass",
			held:     []HeldPermission{{OperationType: "UpdatePermission", Scope: "any", Origin: "runtime"}},
			wantHeld: false,
		},
		{
			// The mixed holding: a refused entry alongside a good one must not
			// poison the good one. requesterHolds scans, so the package entry
			// is found even though the runtime entry precedes it.
			name: "mixed — a refused runtime entry does not retire the package entry",
			held: []HeldPermission{
				{OperationType: laundryReservedOp, Scope: "any", Origin: "runtime"},
				{OperationType: laundryReservedOp, Scope: "any", Origin: "package"},
			},
			wantHeld: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := tc.held[0].OperationType
			if got := requesterHolds(tc.held, op, "any"); got != tc.wantHeld {
				t.Fatalf("requesterHolds(%s) = %v, want %v", op, got, tc.wantHeld)
			}
		})
	}
}

// TestValidateCapabilityArtifact_GrantOfReservedOpFromRuntimeOrigin_Rejected
// drives the invariant through the real validation entry point — the function
// the bridge and the Loupe/CLI approve paths actually call — rather than
// through requesterHolds alone, since that is where a caller could otherwise
// reintroduce the hole.
func TestValidateCapabilityArtifact_GrantOfReservedOpFromRuntimeOrigin_Rejected(t *testing.T) {
	content := grantContent(t, GrantArtifactContent{
		OperationType: laundryReservedOp,
		Scope:         "any",
		GrantsTo:      []string{"front-desk"},
	})

	// Positive vector first: the same proposal, from a package-origin holding,
	// validates. Without this the rejection below could pass on a fixture that
	// rejects every grant of this op for some unrelated reason.
	pkgHeld := []HeldPermission{{OperationType: laundryReservedOp, Scope: "any", Origin: "package"}}
	report, err := ValidateCapabilityArtifact("grant", content, fullCypherParser{}, pkgHeld, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("control BROKEN — a package-origin holding must still support proposing "+
			"a grant of %s; got errors: %v", laundryReservedOp, report.Errors)
	}

	// The attack: the identical proposal, backed only by a self-minted holding
	// the Processor refuses.
	runtimeHeld := []HeldPermission{{OperationType: laundryReservedOp, Scope: "any", Origin: "runtime"}}
	report, err = ValidateCapabilityArtifact("grant", content, fullCypherParser{}, runtimeHeld, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("EXPOSED — a runtime-origin holding of %s (refused at step 3) validated a "+
			"proposal to grant the same op. Approving it re-mints the permission through "+
			"Installer.Apply, which stamps origin:\"package\", laundering the refused grant "+
			"into an authorized one and defeating the reservation entirely.", laundryReservedOp)
	}
}

// The same laundering shape for UpdatePermission, whose reservation is what
// protects the write-once precondition the rest of the design rests on. If a
// self-minted UpdatePermission could be laundered into a package-origin grant,
// its holder could rewrite any permission vertex body — including stripping
// the origin stamp off a package's reserved grant.
func TestValidateCapabilityArtifact_GrantOfUpdatePermissionFromRuntimeOrigin_Rejected(t *testing.T) {
	content := grantContent(t, GrantArtifactContent{
		OperationType: "UpdatePermission",
		Scope:         "any",
		GrantsTo:      []string{"front-desk"},
	})
	held := []HeldPermission{{OperationType: "UpdatePermission", Scope: "any", Origin: "runtime"}}

	report, err := ValidateCapabilityArtifact("grant", content, fullCypherParser{}, held, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatal("EXPOSED — a self-minted UpdatePermission holding validated a proposal to " +
			"grant it; laundering that through an install would hand its holder the ability " +
			"to rewrite any permission vertex's body, including forging origin stamps")
	}
}
