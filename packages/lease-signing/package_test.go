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

// TestPackage_TaskLegDescriptorsNameTheTaskPath is the self-voice test's twin,
// for the two legs a person reaches ONLY through a task.
//
// Neither SignLease nor SignRenewal carries a consumer grant at all — the grant
// matrix gives both to `operator` alone — so the applicant/tenant who actually
// performs them holds no standing grant and is authorized solely by the §10.7
// ephemeral task grant, which step 3 matches on {task, target}. A descriptor
// naming "standing" or "self" here is not a stylistic slip: it tells a
// descriptor-driven client to send the wrong authContext (or none at all), and
// every submission is refused. The client reading these two rows out of the
// op-catalog read model is loftspace-app's task modal, which renders from the
// descriptor rather than from a hardcoded map — so this value IS the behaviour,
// not documentation of it.
func TestPackage_TaskLegDescriptorsNameTheTaskPath(t *testing.T) {
	taskLegs := map[string]string{
		"SignLease":   "leaseapp",
		"SignRenewal": "renewal",
	}
	byOp := map[string]pkgmgr.OpMetaSpec{}
	for _, m := range Package.OpMetas {
		byOp[m.OperationType] = m
	}
	for op, wantTarget := range taskLegs {
		m, ok := byOp[op]
		if !ok {
			t.Fatalf("%s: no op-meta — a task leg a person completes must be self-describing (S1/§15)", op)
		}
		if m.Presentation == nil || m.Presentation.Title == "" || m.InputSchema == "" ||
			len(m.FieldDescriptions) == 0 || m.Dispatch == nil {
			t.Fatalf("%s: needs a FULL descriptor (presentation+schema+fields+dispatch), got %+v", op, m)
		}
		if m.Dispatch.AuthContext != "task" {
			t.Fatalf("%s: authContext = %q, want task — the performer holds no standing grant, so any other value sends an authContext step 3 cannot match and the submission is refused", op, m.Dispatch.AuthContext)
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

// TestPackage_EngineLegsStayBare is the complement: the externalTask legs
// exist so forOperation resolves, not to be rendered — none carries
// Presentation, so none is ever offered to a person. CreateLeaseDocInstance
// is the one exception to BARE (below), not to un-rendered: it carries an
// InputSchema + Dispatch so the descriptor floor can tolerate its
// subject's .tenantName being absent, but still no Presentation.
func TestPackage_EngineLegsStayBare(t *testing.T) {
	engineLegs := []string{
		"CreateLeaseServiceInstance",
		"RecordLeaseServiceOutcome", "RecordServiceDispatch",
		"RecordLeaseDocOutcome",
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

// TestPackage_CreateLeaseDocInstanceFloorsTenantNameAbsence pins the one
// engine leg that is NOT bare: CreateLeaseDocInstance carries no
// Presentation (never offered to a person — Loom's relay actor is the only
// submitter, guarded in the DDL script itself), but does carry an InputSchema
// (guaranteeing subjectKey is present, which is what makes the Dispatch
// template below well-formed per lint-package-standard's checkReadTemplates)
// and a Dispatch declaring {payload.subjectKey}.tenantName as an
// OptionalRead — the descriptor floor that makes a signed application with
// no .tenantName snapshot render instead of failing HydrationMiss.
func TestPackage_CreateLeaseDocInstanceFloorsTenantNameAbsence(t *testing.T) {
	var m pkgmgr.OpMetaSpec
	found := false
	for _, om := range Package.OpMetas {
		if om.OperationType == "CreateLeaseDocInstance" {
			m = om
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("CreateLeaseDocInstance: op-meta missing")
	}
	if m.Presentation != nil {
		t.Fatalf("CreateLeaseDocInstance: Presentation must be nil — it is never offered to a person")
	}
	if m.InputSchema == "" {
		t.Fatalf("CreateLeaseDocInstance: InputSchema must be declared — it is what guarantees subjectKey for the Dispatch template below")
	}
	if m.Dispatch == nil {
		t.Fatalf("CreateLeaseDocInstance: Dispatch must be declared")
	}
	wantOptional := "{payload.subjectKey}.tenantName"
	found = false
	for _, r := range m.Dispatch.OptionalReads {
		if r == wantOptional {
			found = true
		}
	}
	if !found {
		t.Fatalf("CreateLeaseDocInstance: Dispatch.OptionalReads = %v, want to contain %q", m.Dispatch.OptionalReads, wantOptional)
	}
}

// TestPackage_StructurePins pins every declared element by count and canonical
// name (Vertical Package Standard S6, loftspace-domain/package_test.go idiom). A
// declaration added or dropped without a deliberate edit here reds this test
// rather than reaching an install, where the same change is a silent capability
// or read-model shift.
//
// This is the largest package in the corpus and had neither a pin nor a verify
// script, inversely to its size — the census called that out. Two things here are
// load-bearing beyond their counts:
//
//   - The permissions are pinned as (op, scope) PAIRS, because a permission IS
//     its pair (Contract #8 §8.1). Seven ops carry both an operator scope=any
//     grant and a consumer scope=self one; losing a self row would remove
//     self-service while a count-only pin still matched.
//   - Three of the eight lenses are PROTECTED Postgres read models. A lens quietly
//     losing Protected would move identity-bearing rows onto an open surface, so
//     the flag is pinned per lens, not just the lens name.
func TestPackage_StructurePins(t *testing.T) {
	if got, want := len(Package.DDLs), 15; got != want {
		t.Errorf("DDLs: got %d, want %d", got, want)
	}
	if got, want := len(Package.Lenses), 11; got != want {
		t.Errorf("Lenses: got %d, want %d", got, want)
	}
	if got, want := len(Package.Permissions), 26; got != want {
		t.Errorf("Permissions: got %d, want %d", got, want)
	}
	if got, want := len(Package.OpMetas), 16; got != want {
		t.Errorf("OpMetas: got %d, want %d", got, want)
	}
	if got, want := len(Package.Roles), 0; got != want {
		t.Errorf("Roles: got %d, want %d", got, want)
	}
	if got, want := len(Package.WeaverTargets), 8; got != want {
		t.Errorf("WeaverTargets: got %d, want %d", got, want)
	}
	if got, want := len(Package.LoomPatterns), 4; got != want {
		t.Errorf("LoomPatterns: got %d, want %d", got, want)
	}
	wantDeps := []string{"identity-domain", "service-domain", "orchestration-base"}
	if len(Package.Depends) != len(wantDeps) {
		t.Errorf("Depends: got %v, want %v", Package.Depends, wantDeps)
	}
	for i, want := range wantDeps {
		if i < len(Package.Depends) && Package.Depends[i] != want {
			t.Errorf("Depends[%d]: got %q, want %q", i, Package.Depends[i], want)
		}
	}

	wantDDLs := []struct{ name, class string }{
		{"leaseapp", "meta.ddl.vertexType"},
		{"applicantProfile", "meta.ddl.aspectType"},
		{"underwritingParties", "meta.ddl.aspectType"},
		{"applicationSignals", "meta.ddl.aspectType"},
		{"decidedProfileSnapshot", "meta.ddl.aspectType"},
		{"tenantName", "meta.ddl.aspectType"},
		{"leaseServiceInstance", "meta.ddl.vertexType"},
		{"leaseServiceReply", "meta.ddl.vertexType"},
		{"leaseServiceDispatch", "meta.ddl.vertexType"},
		{"leaseServiceOutcome", "meta.ddl.aspectType"},
		{"leaseServiceDispatchMarker", "meta.ddl.aspectType"},
		{"leaseDocInstance", "meta.ddl.vertexType"},
		{"leaseDocReply", "meta.ddl.vertexType"},
		{"leaseDocOutcome", "meta.ddl.aspectType"},
		{"renewal", "meta.ddl.vertexType"},
	}
	for i, want := range wantDDLs {
		if i >= len(Package.DDLs) {
			break
		}
		got := Package.DDLs[i]
		if got.CanonicalName != want.name || got.Class != want.class {
			t.Errorf("DDLs[%d]: got %s/%s, want %s/%s", i, got.CanonicalName, got.Class, want.name, want.class)
		}
	}

	wantLenses := []struct {
		name      string
		adapter   string
		protected bool
	}{
		{"leaseApplicationComplete", "nats-kv", false},
		{"applicantOnboarding", "nats-kv", false},
		{"staleUserTasks", "nats-kv", false},
		{"backgroundCheckFreshness", "nats-kv", false},
		{"supersededBackgroundChecks", "nats-kv", false},
		{"leaseApplicationsRead", "postgres", true},
		{"landlordLeaseApplicationsRead", "postgres", true},
		{"leaseExpiry", "nats-kv", false},
		{"renewalComplete", "nats-kv", false},
		{"renewalsRead", "postgres", true},
		{"tenancyEnd", "nats-kv", false},
	}
	for i, want := range wantLenses {
		if i >= len(Package.Lenses) {
			break
		}
		got := Package.Lenses[i]
		if got.CanonicalName != want.name || got.Adapter != want.adapter || got.Protected != want.protected {
			t.Errorf("Lenses[%d]: got %s/%s/protected=%v, want %s/%s/protected=%v",
				i, got.CanonicalName, got.Adapter, got.Protected, want.name, want.adapter, want.protected)
		}
	}

	wantPerms := []struct{ op, scope string }{
		{"CreateLeaseApplication", "any"}, {"CreateLeaseApplication", "self"},
		{"CreateLeaseServiceInstance", "any"},
		{"TombstoneSupersededLeaseServiceInstance", "any"},
		{"RecordLeaseServiceOutcome", "any"}, {"RecordServiceDispatch", "any"},
		{"CreateLeaseDocInstance", "any"}, {"RecordLeaseDocOutcome", "any"},
		{"SignLease", "any"},
		{"WithdrawLeaseApplication", "any"}, {"WithdrawLeaseApplication", "self"},
		{"DecideLeaseApplication", "any"}, {"DecideLeaseApplication", "self"},
		{"SetApplicantProfile", "any"}, {"SetApplicantProfile", "self"},
		{"BackfillLeaseTerms", "any"},
		{"ReassignLeaseUnit", "any"},
		{"OpenRenewal", "any"},
		{"SetRenewalTerms", "any"}, {"SetRenewalTerms", "self"},
		{"VerifyGuarantor", "any"}, {"VerifyGuarantor", "self"},
		{"SignRenewal", "any"},
		{"CancelRenewal", "any"}, {"CancelRenewal", "self"},
		{"EndTenancy", "any"},
	}
	for i, want := range wantPerms {
		if i >= len(Package.Permissions) {
			break
		}
		got := Package.Permissions[i]
		if got.OperationType != want.op || got.Scope != want.scope {
			t.Errorf("Permissions[%d]: got %s/%s, want %s/%s", i, got.OperationType, got.Scope, want.op, want.scope)
		}
	}
}

// TestPackage_LeaseApplicationCompleteEscalatesExhaustedToAugur proves a gap
// that spends its retry budget (declared maxretries_bgcheck/payment, or the
// engine's default cap for any other gap on this target) reaches the Augur
// AI-reasoning tier instead of parking behind an unread Health-KV
// GapBudgetExhausted warning — internal/weaver's engine-side escalation
// mechanics are proven by TestHandleRow_ExhaustedGapEscalatesToAugur; this
// pins the PACKAGE'S declaration of it.
func TestPackage_LeaseApplicationCompleteEscalatesExhaustedToAugur(t *testing.T) {
	for _, target := range Package.WeaverTargets {
		if target.TargetID != "leaseApplicationComplete" {
			continue
		}
		if target.Augur == nil {
			t.Fatalf("leaseApplicationComplete target: Augur is nil, want an \"exhausted\" escalation")
		}
		if got, want := target.Augur.Escalate, []string{"exhausted"}; len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("leaseApplicationComplete target: Augur.Escalate = %v, want %v", got, want)
		}
		return
	}
	t.Fatal("leaseApplicationComplete target not found in Package.WeaverTargets")
}

// TestPackage_ProfileAndUnderwritingPartiesAreSensitiveAndCustodied is the
// regression guard for the underwriting-record custody posture (mirrors
// clinic-domain's TestPackage_EncounterAspectIsSensitiveAndCustodied): both
// .profile and .underwritingParties must declare Sensitive + a retentionClass
// Custody naming underwritingRecord, and .tenantName must declare Sensitive +
// a retentionClass Custody naming the SEPARATE executedLeaseRecord class — the
// package must declare exactly those two retention classes, both
// eraseOnExpiry. A silent loss of either — Sensitive flipping back to false,
// or Custody being dropped/retargeted — would fall back to committing the
// applicant's raw financials, the guarantor/co-applicant's identifiers, or
// the executed lease's tenant name as PLAINTEXT (Sensitive false), collapse
// the two classes' distinct populations into one (RetentionClass
// misdirected), or reject at install (Custody naming an undeclared class).
func TestPackage_ProfileAndUnderwritingPartiesAreSensitiveAndCustodied(t *testing.T) {
	byName := map[string]pkgmgr.DDLSpec{}
	for _, d := range Package.DDLs {
		byName[d.CanonicalName] = d
	}

	for _, name := range []string{"applicantProfile", "underwritingParties"} {
		asp, ok := byName[name]
		if !ok {
			t.Fatalf("missing %s aspectType DDL", name)
		}
		if !asp.Sensitive {
			t.Fatalf("%s must be Sensitive (it carries retained underwriting data)", name)
		}
		if asp.Custody.Kind != pkgmgr.CustodyKindRetentionClass {
			t.Fatalf("%s Custody.Kind = %q, want %q", name, asp.Custody.Kind, pkgmgr.CustodyKindRetentionClass)
		}
		if asp.Custody.RetentionClass != "underwritingRecord" {
			t.Fatalf("%s Custody.RetentionClass = %q, want %q", name, asp.Custody.RetentionClass, "underwritingRecord")
		}
		if len(asp.PermittedCommands) != 1 || asp.PermittedCommands[0] != "SetApplicantProfile" {
			t.Fatalf("%s PermittedCommands = %v, want [SetApplicantProfile]", name, asp.PermittedCommands)
		}
	}

	tn, ok := byName["tenantName"]
	if !ok {
		t.Fatalf("missing tenantName aspectType DDL")
	}
	if !tn.Sensitive {
		t.Fatalf("tenantName must be Sensitive (it carries the executed lease's party name)")
	}
	if tn.Custody.Kind != pkgmgr.CustodyKindRetentionClass {
		t.Fatalf("tenantName Custody.Kind = %q, want %q", tn.Custody.Kind, pkgmgr.CustodyKindRetentionClass)
	}
	if tn.Custody.RetentionClass != "executedLeaseRecord" {
		t.Fatalf("tenantName Custody.RetentionClass = %q, want %q (NOT underwritingRecord — a different obligation and population)", tn.Custody.RetentionClass, "executedLeaseRecord")
	}
	if len(tn.PermittedCommands) != 1 || tn.PermittedCommands[0] != "SignLease" {
		t.Fatalf("tenantName PermittedCommands = %v, want [SignLease]", tn.PermittedCommands)
	}

	if got := len(Package.RetentionClasses); got != 2 {
		t.Fatalf("expected exactly 2 retention classes, got %d", got)
	}
	rcByName := map[string]pkgmgr.RetentionClassSpec{}
	for _, rc := range Package.RetentionClasses {
		rcByName[rc.CanonicalName] = rc
	}
	for _, name := range []string{"underwritingRecord", "executedLeaseRecord"} {
		rc, ok := rcByName[name]
		if !ok {
			t.Fatalf("missing retention class %q", name)
		}
		if rc.Policy != pkgmgr.RetentionPolicyEraseOnExpiry {
			t.Fatalf("retention class %s Policy = %q, want %q", name, rc.Policy, pkgmgr.RetentionPolicyEraseOnExpiry)
		}
		if rc.RetentionPeriod == "" {
			t.Fatalf("retention class %s RetentionPeriod must be declared (it is declarative, but an unstated schedule is unauditable)", name)
		}
		if rc.Description == "" {
			t.Fatalf("retention class %s Description must be declared", name)
		}
	}
}

// TestPackage_ApplicationSignalsIsNotSensitive proves the split's whole
// point: the operational .applicationSignals aspect carries no custody at
// all, so the three plain (unencrypted-read) lenses that project it can
// actually read it. A regression that flips Sensitive on this DDL would make
// leaseApplicationComplete/leaseApplicationsRead/landlordLeaseApplicationsRead's
// qualification-signal columns unreadable by every non-Vault-aware lens.
func TestPackage_ApplicationSignalsIsNotSensitive(t *testing.T) {
	byName := map[string]pkgmgr.DDLSpec{}
	for _, d := range Package.DDLs {
		byName[d.CanonicalName] = d
	}
	sig, ok := byName["applicationSignals"]
	if !ok {
		t.Fatalf("missing applicationSignals aspectType DDL")
	}
	if sig.Sensitive {
		t.Fatalf("applicationSignals must NOT be sensitive — it is the plain-lens-readable half of the split")
	}
	if sig.Custody.Kind != "" || sig.Custody.RetentionClass != "" {
		t.Fatalf("applicationSignals must declare no custody, got %+v", sig.Custody)
	}
}
