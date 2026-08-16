package pkgmgr

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// newCapabilityApplyHarness boots an embedded NATS with nothing but the Core
// KV bucket: CapabilityApplyPlanForProposal is read-only, so the plan builder
// needs no Processor pipeline, no primordials and no installed packages —
// deliberately leaner than newInstallerHarness, which stands up the whole
// install path.
func newCapabilityApplyHarness(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: s.ClientURL(), Name: "pkgmgr-capabilityapply-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         CoreBucket,
		LimitMarkerTTL: time.Second,
	}); err != nil {
		t.Fatalf("create %s bucket: %v", CoreBucket, err)
	}
	return ctx, conn
}

// guardProposalID mints a distinct 20-char NanoID-alphabet proposal id per
// table case (n < len(keys.Alphabet)).
func guardProposalID(t *testing.T, n int) string {
	t.Helper()
	if n >= len(keys.Alphabet) {
		t.Fatalf("guardProposalID: %d exceeds the single-suffix-char id space", n)
	}
	return "CAGuardHJKMNPQRSTUV" + string(keys.Alphabet[n])
}

// seedApprovedProposal writes the three aspects CapabilityApplyPlanForProposal
// reads — .review (approved), .artifact (a well-formed lens, so the artifact
// side of the plan is never what fails) and .target — straight into Core KV in
// the {isDeleted,data} envelope readAspectData expects, and returns the
// proposal key.
func seedApprovedProposal(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalID, packageName, mode string) string {
	t.Helper()
	proposalKey := "vtx.capabilityproposal." + proposalID

	content, err := json.Marshal(LensArtifactContent{
		CanonicalName: "capApplyGuardLens",
		Adapter:       "nats-kv",
		Bucket:        "cap-apply-guard",
		Spec:          "MATCH (p:provider) RETURN p.key AS key",
	})
	if err != nil {
		t.Fatalf("marshal lens content: %v", err)
	}

	aspects := map[string]map[string]any{
		proposalKey + ".review":   {"state": "approved"},
		proposalKey + ".artifact": {"kind": "lens", "content": string(content)},
		proposalKey + ".target":   {"packageName": packageName, "mode": mode},
	}
	for key, data := range aspects {
		b, err := json.Marshal(map[string]any{"isDeleted": false, "data": data})
		if err != nil {
			t.Fatalf("marshal %s: %v", key, err)
		}
		if _, err := conn.KVPut(ctx, CoreBucket, key, b); err != nil {
			t.Fatalf("KVPut %s: %v", key, err)
		}
	}
	return proposalKey
}

// TestCapabilityApplyPlan_PlatformProtectedPackage_Rejected: every
// platform-protected package name is refused in BOTH modes, on an otherwise
// perfectly applicable approved proposal (approved review, well-formed lens
// artifact). The refusal must not depend on the live install catalog — none of
// these packages is installed in this harness, so an upgradeExisting case that
// slipped past the guard would fail with the "no package by that name is
// installed" message instead, which the assertion rejects.
func TestCapabilityApplyPlan_PlatformProtectedPackage_Rejected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)

	protected := make([]string, 0, len(platformProtectedPackages))
	for name := range platformProtectedPackages {
		protected = append(protected, name)
	}
	sort.Strings(protected)

	n := 0
	for _, mode := range []string{"newPackage", "upgradeExisting"} {
		for _, packageName := range protected {
			proposalID := guardProposalID(t, n)
			n++
			t.Run(mode+"/"+packageName, func(t *testing.T) {
				proposalKey := seedApprovedProposal(t, ctx, conn, proposalID, packageName, mode)

				plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
				if err == nil {
					t.Fatalf("CapabilityApplyPlanForProposal(%s as %s) returned plan %+v, want a platform-protected refusal", packageName, mode, plan)
				}
				if !strings.Contains(err.Error(), "platform-protected") || !strings.Contains(err.Error(), packageName) {
					t.Fatalf("error = %v, want one naming %q as platform-protected", err, packageName)
				}
			})
		}
	}
}

// TestPlatformProtectedPackage_Normalizes: the deny-list is consulted through
// a normalizing predicate, so a near-miss spelling of a protected name cannot
// walk past an exact-byte map lookup — which would land an AI-authored package
// under a name indistinguishable from the real one in Loupe's package list.
// Normalization is case + surrounding whitespace ONLY: it must never widen
// into substring or prefix matching, or a legitimately distinct package whose
// name merely extends a protected one would be refused.
func TestPlatformProtectedPackage_Normalizes(t *testing.T) {
	protected := []string{
		"rbac-domain",
		"Rbac-Domain",
		"RBAC-DOMAIN",
		" rbac-domain ",
		"rbac-domain\t",
		"\n identity-hygiene ",
		"Demo-Operator",
	}
	for _, name := range protected {
		if !PlatformProtectedPackage(name) {
			t.Errorf("PlatformProtectedPackage(%q) = false, want true", name)
		}
	}

	allowed := []string{
		"rbac-domain-extended",
		"my-rbac-domain",
		"rbac",
		"cafe-domain",
		"",
	}
	for _, name := range allowed {
		if PlatformProtectedPackage(name) {
			t.Errorf("PlatformProtectedPackage(%q) = true, want false — the guard must not over-match", name)
		}
	}
}

// TestCapabilityApplyPlan_NearMissProtectedName_Rejected: the normalization is
// wired into the plan builder's own check, not just the exported predicate — a
// proposal declaring a whitespace/case variant of a protected name is refused
// there too, in the mode (newPackage on a name nothing has installed) that
// would otherwise sail through the live-catalog binding and build a Definition.
func TestCapabilityApplyPlan_NearMissProtectedName_Rejected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)

	for n, packageName := range []string{"Rbac-Domain", " rbac-domain ", "rbac-domain\t"} {
		t.Run(packageName, func(t *testing.T) {
			proposalKey := seedApprovedProposal(t, ctx, conn, guardProposalID(t, n), packageName, "newPackage")

			plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
			if err == nil {
				t.Fatalf("CapabilityApplyPlanForProposal(%q) returned plan %+v, want a platform-protected refusal", packageName, plan)
			}
			if !strings.Contains(err.Error(), "platform-protected") {
				t.Fatalf("error = %v, want a platform-protected refusal", err)
			}
		})
	}
}

// TestCapabilityApplyPlan_ProtectedPrefixName_Unaffected: a genuinely distinct
// package whose name merely extends a protected one still builds — the guard
// is an exact (normalized) name match, never a prefix one.
func TestCapabilityApplyPlan_ProtectedPrefixName_Unaffected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)

	proposalKey := seedApprovedProposal(t, ctx, conn, "CAGuardExtendedHJKMN", "rbac-domain-extended", "newPackage")
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal(rbac-domain-extended): %v", err)
	}
	if plan.PackageName != "rbac-domain-extended" {
		t.Fatalf("plan.PackageName = %q, want rbac-domain-extended", plan.PackageName)
	}
}

// TestCapabilityApplyPlan_VerticalPackage_Unaffected: a vertical
// business-domain name is NOT on the deny-list — the plan builds through to a
// real Definition (newPackage on a name nothing has installed), which is
// exactly the case this guard must leave working.
func TestCapabilityApplyPlan_VerticalPackage_Unaffected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)

	proposalKey := seedApprovedProposal(t, ctx, conn, "CAGuardVerticalHJKMN", "cafe-domain", "newPackage")
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal(cafe-domain): %v", err)
	}
	if plan.PackageName != "cafe-domain" {
		t.Fatalf("plan.PackageName = %q, want cafe-domain", plan.PackageName)
	}
	if len(plan.Definition.Lenses) != 1 {
		t.Fatalf("plan.Definition.Lenses = %d, want 1", len(plan.Definition.Lenses))
	}
}
