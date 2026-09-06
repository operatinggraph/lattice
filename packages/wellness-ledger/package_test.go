package wellnessledger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition keeps manifest.yaml and the Go Definition
// in lockstep (the loftspace-ledger precedent): the install reads the Definition,
// but the manifest is the human-facing declaration, and a drift between the two
// is a silent install hazard.
func TestPackage_ManifestMatchesDefinition(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	m, err := pkgmgr.ParseManifest(filepath.Join(wd, "manifest.yaml"))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := m.VerifyAgainstDefinition(Package); err != nil {
		t.Fatalf("manifest <-> Definition drift: %v", err)
	}
}

// TestPackage_StructurePins pins what this package declares, by count and by
// canonical name (Vertical Package Standard S6, loftspace-domain/package_test.go
// idiom). A declaration added or dropped without a deliberate edit here reds
// this test rather than reaching an install, where the same change is a silent
// capability or read-model shift.
func TestPackage_StructurePins(t *testing.T) {
	if got, want := len(Package.DDLs), 3; got != want {
		t.Errorf("DDLs: got %d, want %d", got, want)
	}
	if got, want := len(Package.Permissions), 5; got != want {
		t.Errorf("Permissions: got %d, want %d", got, want)
	}
	if got, want := len(Package.Lenses), 5; got != want {
		t.Errorf("Lenses: got %d, want %d", got, want)
	}
	if got, want := len(Package.WeaverTargets), 3; got != want {
		t.Errorf("WeaverTargets: got %d, want %d", got, want)
	}
	if got, want := len(Package.LoomPatterns), 0; got != want {
		t.Errorf("LoomPatterns: got %d, want %d", got, want)
	}
	if got, want := len(Package.OpMetas), 3; got != want {
		t.Errorf("OpMetas: got %d, want %d", got, want)
	}

	wantDDLs := []string{"wellnessaccount", "wellnessLedgerAccountGuard", "wellnesstransaction"}
	for i, d := range Package.DDLs {
		if i < len(wantDDLs) && d.CanonicalName != wantDDLs[i] {
			t.Errorf("DDLs[%d]: got %q, want %q", i, d.CanonicalName, wantDDLs[i])
		}
	}

	wantPerms := []struct{ op, scope string }{{"WellnessCreateAccount", "any"}, {"WellnessCreateAccount", "self"}, {"WellnessDebitAccount", "any"}, {"WellnessCreditAccount", "any"}, {"WellnessCreditAccount", "self"}}
	for i, want := range wantPerms {
		if i >= len(Package.Permissions) {
			break
		}
		got := Package.Permissions[i]
		if got.OperationType != want.op || got.Scope != want.scope {
			t.Errorf("Permissions[%d]: got %s/%s, want %s/%s", i, got.OperationType, got.Scope, want.op, want.scope)
		}
	}

	wantLenses := []string{"wellnessLedgerHistory", "wellnessMemberAccounts", "wellnessNoShowSettlement", "wellnessClassPriceSettlement", "wellnessRefundSettlement"}
	for i, d := range Package.Lenses {
		if i < len(wantLenses) && d.CanonicalName != wantLenses[i] {
			t.Errorf("Lenses[%d]: got %q, want %q", i, d.CanonicalName, wantLenses[i])
		}
	}

	wantTargets := []string{"wellnessNoShowSettlement", "wellnessClassPriceSettlement", "wellnessRefundSettlement"}
	for i, d := range Package.WeaverTargets {
		if i < len(wantTargets) && d.TargetID != wantTargets[i] {
			t.Errorf("WeaverTargets[%d]: got %q, want %q", i, d.TargetID, wantTargets[i])
		}
	}
}

// TestPackage_ClassPriceSettlementEscalatesExhaustedToAugur proves a gap
// that spends its retry budget (declared maxretries_price_charge, or the
// engine's default cap for any other gap on this target) reaches the Augur
// AI-reasoning tier instead of parking behind an unread Health-KV
// GapBudgetExhausted warning — internal/weaver's engine-side escalation
// mechanics are proven by TestHandleRow_ExhaustedGapEscalatesToAugur; this
// pins the PACKAGE'S declaration of it (mirrors lease-signing's
// TestPackage_LeaseApplicationCompleteEscalatesExhaustedToAugur).
func TestPackage_ClassPriceSettlementEscalatesExhaustedToAugur(t *testing.T) {
	for _, target := range Package.WeaverTargets {
		if target.TargetID != ClassPriceSettlementTarget {
			continue
		}
		if target.Augur == nil {
			t.Fatalf("%s target: Augur is nil, want an \"exhausted\" escalation", ClassPriceSettlementTarget)
		}
		if got, want := target.Augur.Escalate, []string{"exhausted"}; len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("%s target: Augur.Escalate = %v, want %v", ClassPriceSettlementTarget, got, want)
		}
		return
	}
	t.Fatalf("%s target not found in Package.WeaverTargets", ClassPriceSettlementTarget)
}

// TestPackage_RefundSettlementPostsRefundReason pins the missing_refund gap's
// dispatched WellnessCreditAccount Params to carry reason:"refund" (a plain
// string literal, not a "row." template — strategist.go's resolveParam only
// templates a "row." prefix or decodes a "json:" literal) — without it, a
// Weaver-settled refund posts as an indistinguishable reason:"payment"
// credit, and a member's statement can never tell a refund from cash handed
// over at the front desk.
func TestPackage_RefundSettlementPostsRefundReason(t *testing.T) {
	for _, target := range Package.WeaverTargets {
		if target.TargetID != RefundSettlementTarget {
			continue
		}
		gap, ok := target.Gaps["missing_refund"]
		if !ok {
			t.Fatalf("%s target: no missing_refund gap declared", RefundSettlementTarget)
		}
		if got, want := gap.Params["reason"], "refund"; got != want {
			t.Fatalf("%s target: missing_refund gap Params[\"reason\"] = %q, want %q", RefundSettlementTarget, got, want)
		}
		return
	}
	t.Fatalf("%s target not found in Package.WeaverTargets", RefundSettlementTarget)
}
