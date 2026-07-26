package leasesigning

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition keeps manifest.yaml and the Go Definition
// in lockstep: the install reads the Definition, but the manifest is the
// human-facing declaration, and a drift between the two (a permission / op added to
// one but not the other) is a silent install hazard. VerifyAgainstDefinition
// cross-checks name, version, and the declared DDL/lens/permission/weaverTarget/
// loomPattern/opMeta listings.
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

// TestPackage_SelfServiceDescriptorsNameTheSelfPath pins the dispatch shape of
// every op a real person triggers here.
//
// The applicant's three legs and the landlord's four are all reachable at
// scope=self. A descriptor-driven client builds its whole submission from the
// op-meta, so an authContext of "standing" makes it send no authContext object
// at all — which lands a landlord on the staff path and gets them refused.
// DecideLeaseApplication is the subtle one: it carries BOTH an operator
// scope=any grant and a landlord scope=self one, and a single descriptor has to
// choose. It names the self path, because that is the one a client cannot
// infer; a staff FE hardcodes its own standing submit.
//
// The gate (scripts/lint-package-standard.go) enforces that a full descriptor
// EXISTS but says nothing about which path it names, so without this test the
// value is free to drift.
func TestPackage_SelfServiceDescriptorsNameTheSelfPath(t *testing.T) {
	// TargetType is the type of the entity the client resolves the target
	// FIELD from — not the op's own class. CreateLeaseApplication is the one
	// that differs: it targets the UNIT in view, because the application it
	// creates does not exist yet to be targeted.
	selfService := map[string]string{
		"CreateLeaseApplication":   "unit",
		"WithdrawLeaseApplication": "leaseapp",
		"SetApplicantProfile":      "leaseapp",
		"DecideLeaseApplication":   "leaseapp",
		"SetRenewalTerms":          "renewal",
		"VerifyGuarantor":          "renewal",
		"CancelRenewal":            "renewal",
	}
	byOp := map[string]pkgmgr.OpMetaSpec{}
	for _, m := range Package.OpMetas {
		byOp[m.OperationType] = m
	}
	for op, wantTarget := range selfService {
		m, ok := byOp[op]
		if !ok {
			t.Fatalf("%s: no op-meta — a granted self-service op must be self-describing (S1)", op)
		}
		if m.Presentation == nil || m.Presentation.Title == "" || m.InputSchema == "" ||
			len(m.FieldDescriptions) == 0 || m.Dispatch == nil {
			t.Fatalf("%s: needs a FULL descriptor (presentation+schema+fields+dispatch), got %+v", op, m)
		}
		if m.Dispatch.AuthContext != "self" {
			t.Fatalf("%s: authContext = %q, want self — a standing descriptor sends no authContext and the self path is refused", op, m.Dispatch.AuthContext)
		}
		if m.Dispatch.TargetType != wantTarget {
			t.Fatalf("%s: targetType = %q, want %q", op, m.Dispatch.TargetType, wantTarget)
		}
		if m.Dispatch.TargetField == "" {
			t.Fatalf("%s: a named targetType needs the field it resolves into", op)
		}
		if len(m.Dispatch.Reads) == 0 {
			t.Fatalf("%s: declares no reads — the submitter lists exact keys (Contract #2 §2.5)", op)
		}
	}
}

// TestPackage_EngineLegsStayBare is the complement: the externalTask and
// assignTask legs exist so forOperation resolves, not to be rendered. Giving
// one a descriptor would offer a client a form for an op no human submits.
func TestPackage_EngineLegsStayBare(t *testing.T) {
	engineLegs := []string{
		"SignLease", "RecordIdentityPII", "CreateLeaseServiceInstance",
		"RecordLeaseServiceOutcome", "RecordServiceDispatch",
		"CreateLeaseDocInstance", "RecordLeaseDocOutcome", "SignRenewal",
	}
	byOp := map[string]pkgmgr.OpMetaSpec{}
	for _, m := range Package.OpMetas {
		byOp[m.OperationType] = m
	}
	for _, op := range engineLegs {
		m, ok := byOp[op]
		if !ok {
			t.Fatalf("%s: op-meta missing — forOperation resolution needs it", op)
		}
		if m.Presentation != nil || m.InputSchema != "" || m.Dispatch != nil {
			t.Fatalf("%s: expected a bare forOperation meta, got a descriptor: %+v", op, m)
		}
	}
}
