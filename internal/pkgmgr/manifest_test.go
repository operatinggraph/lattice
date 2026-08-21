package pkgmgr

import (
	"strings"
	"testing"
)

// TestParseManifestBytes_HappyPath parses the canonical identity-hygiene
// manifest example from the spec. Pure unit test — no NATS required.
func TestParseManifestBytes_HappyPath(t *testing.T) {
	raw := []byte(`name: identity-hygiene
version: 0.1.0
description: test
depends:
  - identity-domain
declares:
  ddls:
    - canonicalName: identityHygiene
      class: meta.ddl.vertexType
  lenses:
    - canonicalName: duplicateCandidates
      adapter: nats-kv
      bucket: duplicate-candidates
      engine: full
  permissions:
    - operationType: MergeIdentity
      scope: any
      grantsTo: [operator]
`)
	m, err := ParseManifestBytes(raw)
	if err != nil {
		t.Fatalf("ParseManifestBytes: %v", err)
	}
	if m.Name != "identity-hygiene" {
		t.Errorf("name = %q, want identity-hygiene", m.Name)
	}
	if m.Version != "0.1.0" {
		t.Errorf("version = %q, want 0.1.0", m.Version)
	}
	if len(m.Declares.DDLs) != 1 || m.Declares.DDLs[0].CanonicalName != "identityHygiene" {
		t.Errorf("DDLs = %+v", m.Declares.DDLs)
	}
	if len(m.Declares.Lenses) != 1 || m.Declares.Lenses[0].Bucket != "duplicate-candidates" {
		t.Errorf("Lenses = %+v", m.Declares.Lenses)
	}
	if len(m.Declares.Permissions) != 1 || m.Declares.Permissions[0].OperationType != "MergeIdentity" {
		t.Errorf("Permissions = %+v", m.Declares.Permissions)
	}
}

// TestParseManifestBytes_RequiredFields rejects missing name / version.
func TestParseManifestBytes_RequiredFields(t *testing.T) {
	cases := map[string]string{
		"missing-name":    "version: 0.1.0\ndeclares: {}\n",
		"missing-version": "name: foo\ndeclares: {}\n",
	}
	for label, raw := range cases {
		_, err := ParseManifestBytes([]byte(raw))
		if err == nil {
			t.Errorf("%s: expected error, got nil", label)
		}
	}
}

// TestVerifyAgainstDefinition_HappyPath asserts manifest <-> Go
// definition cross-validation passes when they match.
func TestVerifyAgainstDefinition_HappyPath(t *testing.T) {
	m := &Manifest{
		Name:    "x",
		Version: "1.0",
		Declares: ManifestBlock{
			DDLs:        []ManifestDDL{{CanonicalName: "A"}},
			Lenses:      []ManifestLens{{CanonicalName: "L"}},
			Permissions: []ManifestPermission{{OperationType: "Op"}},
		},
	}
	def := Definition{
		Name:        "x",
		Version:     "1.0",
		DDLs:        []DDLSpec{{CanonicalName: "A"}},
		Lenses:      []LensSpec{{CanonicalName: "L"}},
		Permissions: []PermissionSpec{{OperationType: "Op"}},
	}
	if err := m.VerifyAgainstDefinition(def); err != nil {
		t.Fatalf("VerifyAgainstDefinition: %v", err)
	}
}

// TestVerifyAgainstDefinition_NameMismatch surfaces the typo a package
// author makes when they rename one but not the other.
func TestVerifyAgainstDefinition_NameMismatch(t *testing.T) {
	m := &Manifest{Name: "x", Version: "1.0"}
	def := Definition{Name: "y", Version: "1.0"}
	err := m.VerifyAgainstDefinition(def)
	if err == nil || !strings.Contains(err.Error(), "manifest.name") {
		t.Fatalf("expected name-mismatch error, got %v", err)
	}
}

// TestVerifyAgainstDefinition_CountMismatch surfaces the case where a
// package author updates one source but not the other.
func TestVerifyAgainstDefinition_CountMismatch(t *testing.T) {
	m := &Manifest{
		Name:     "x",
		Version:  "1.0",
		Declares: ManifestBlock{DDLs: []ManifestDDL{{CanonicalName: "A"}}},
	}
	def := Definition{
		Name:    "x",
		Version: "1.0",
		DDLs:    []DDLSpec{{CanonicalName: "A"}, {CanonicalName: "B"}},
	}
	err := m.VerifyAgainstDefinition(def)
	if err == nil || !strings.Contains(err.Error(), "declares 1 DDLs but Definition has 2") {
		t.Fatalf("expected count-mismatch error, got %v", err)
	}
}

// TestVerifyAgainstDefinition_OrchestrationHappyPath asserts the new
// orchestration kinds cross-check cleanly when manifest and Definition agree.
func TestVerifyAgainstDefinition_OrchestrationHappyPath(t *testing.T) {
	m := &Manifest{
		Name:    "x",
		Version: "1.0",
		Declares: ManifestBlock{
			WeaverTargets: []ManifestWeaverTarget{{TargetID: "T"}},
			LoomPatterns:  []ManifestLoomPattern{{PatternID: "P"}},
			OpMetas:       []ManifestOpMeta{{OperationType: "Op"}},
		},
	}
	def := Definition{
		Name:          "x",
		Version:       "1.0",
		WeaverTargets: []WeaverTargetSpec{{TargetID: "T"}},
		LoomPatterns:  []LoomPatternSpec{{PatternID: "P"}},
		OpMetas:       []OpMetaSpec{{OperationType: "Op"}},
	}
	if err := m.VerifyAgainstDefinition(def); err != nil {
		t.Fatalf("VerifyAgainstDefinition: %v", err)
	}
}

func TestVerifyAgainstDefinition_WeaverTargetCountMismatch(t *testing.T) {
	m := &Manifest{Name: "x", Version: "1.0"}
	def := Definition{Name: "x", Version: "1.0", WeaverTargets: []WeaverTargetSpec{{TargetID: "T"}}}
	err := m.VerifyAgainstDefinition(def)
	if err == nil || !strings.Contains(err.Error(), "weaverTargets") {
		t.Fatalf("expected weaverTargets count-mismatch error, got %v", err)
	}
}

func TestVerifyAgainstDefinition_LoomPatternIdentityMismatch(t *testing.T) {
	m := &Manifest{
		Name:     "x",
		Version:  "1.0",
		Declares: ManifestBlock{LoomPatterns: []ManifestLoomPattern{{PatternID: "P1"}}},
	}
	def := Definition{Name: "x", Version: "1.0", LoomPatterns: []LoomPatternSpec{{PatternID: "P2"}}}
	err := m.VerifyAgainstDefinition(def)
	if err == nil || !strings.Contains(err.Error(), "patternId mismatch") {
		t.Fatalf("expected patternId-mismatch error, got %v", err)
	}
}

func TestVerifyAgainstDefinition_OpMetaIdentityMismatch(t *testing.T) {
	m := &Manifest{
		Name:     "x",
		Version:  "1.0",
		Declares: ManifestBlock{OpMetas: []ManifestOpMeta{{OperationType: "A"}}},
	}
	def := Definition{Name: "x", Version: "1.0", OpMetas: []OpMetaSpec{{OperationType: "B"}}}
	err := m.VerifyAgainstDefinition(def)
	if err == nil || !strings.Contains(err.Error(), "operationType mismatch") {
		t.Fatalf("expected operationType-mismatch error, got %v", err)
	}
}

// TestVerifyAgainstDefinition_RetentionClassCountMismatch surfaces a package
// that mints a retention-class key holder in its Go Definition without
// declaring it in the reviewable manifest.
func TestVerifyAgainstDefinition_RetentionClassCountMismatch(t *testing.T) {
	m := &Manifest{Name: "x", Version: "1.0"}
	def := Definition{
		Name:             "x",
		Version:          "1.0",
		RetentionClasses: []RetentionClassSpec{{CanonicalName: "clinicalRecord"}},
	}
	err := m.VerifyAgainstDefinition(def)
	if err == nil || !strings.Contains(err.Error(), "retentionClasses") {
		t.Fatalf("expected retentionClasses count-mismatch error, got %v", err)
	}
}

func TestVerifyAgainstDefinition_RetentionClassIdentityMismatch(t *testing.T) {
	m := &Manifest{
		Name:     "x",
		Version:  "1.0",
		Declares: ManifestBlock{RetentionClasses: []ManifestRetentionClass{{CanonicalName: "clinicalRecord"}}},
	}
	def := Definition{
		Name:             "x",
		Version:          "1.0",
		RetentionClasses: []RetentionClassSpec{{CanonicalName: "underwritingRecord"}},
	}
	err := m.VerifyAgainstDefinition(def)
	if err == nil || !strings.Contains(err.Error(), "RetentionClass[0] canonicalName mismatch") {
		t.Fatalf("expected RetentionClass canonicalName-mismatch error, got %v", err)
	}
}

// TestVerifyAgainstDefinition_RetentionClassHappyPath asserts a manifest
// that declares the same retention classes as the Definition, in the same
// order — including the Policy/RetentionPeriod obligation fields, not just
// canonicalName — cross-checks cleanly.
func TestVerifyAgainstDefinition_RetentionClassHappyPath(t *testing.T) {
	m := &Manifest{
		Name:    "x",
		Version: "1.0",
		Declares: ManifestBlock{
			RetentionClasses: []ManifestRetentionClass{{CanonicalName: "clinicalRecord", Policy: "eraseOnExpiry", RetentionPeriod: "P7Y"}},
		},
	}
	def := Definition{
		Name:             "x",
		Version:          "1.0",
		RetentionClasses: []RetentionClassSpec{{CanonicalName: "clinicalRecord", Policy: "eraseOnExpiry", RetentionPeriod: "P7Y"}},
	}
	if err := m.VerifyAgainstDefinition(def); err != nil {
		t.Fatalf("VerifyAgainstDefinition: %v", err)
	}
}

// TestVerifyAgainstDefinition_RetentionClassPolicyMismatch is B8's
// regression: the manifest is the reviewable statement of a retention
// class's actual obligation, not just which class exists, so a Policy that
// drifted between the manifest and the Definition must fail verification —
// canonicalName alone identifying the class is not enough.
func TestVerifyAgainstDefinition_RetentionClassPolicyMismatch(t *testing.T) {
	m := &Manifest{
		Name:    "x",
		Version: "1.0",
		Declares: ManifestBlock{
			RetentionClasses: []ManifestRetentionClass{{CanonicalName: "clinicalRecord", Policy: "eraseOnExpiry", RetentionPeriod: "P7Y"}},
		},
	}
	def := Definition{
		Name:             "x",
		Version:          "1.0",
		RetentionClasses: []RetentionClassSpec{{CanonicalName: "clinicalRecord", Policy: "somethingElse", RetentionPeriod: "P7Y"}},
	}
	err := m.VerifyAgainstDefinition(def)
	if err == nil || !strings.Contains(err.Error(), "policy mismatch") {
		t.Fatalf("expected RetentionClass policy-mismatch error, got %v", err)
	}
}

// TestVerifyAgainstDefinition_RetentionClassRetentionPeriodMismatch is B8's
// central claim, proven directly: a manifest whose declared RetentionPeriod
// silently drifted from the Definition's — e.g. an author shortening a
// retention period from years to days without touching the manifest — must
// fail verification, not pass with a zero-line diff.
func TestVerifyAgainstDefinition_RetentionClassRetentionPeriodMismatch(t *testing.T) {
	m := &Manifest{
		Name:    "x",
		Version: "1.0",
		Declares: ManifestBlock{
			RetentionClasses: []ManifestRetentionClass{{CanonicalName: "clinicalRecord", Policy: "eraseOnExpiry", RetentionPeriod: "P7Y"}},
		},
	}
	def := Definition{
		Name:             "x",
		Version:          "1.0",
		RetentionClasses: []RetentionClassSpec{{CanonicalName: "clinicalRecord", Policy: "eraseOnExpiry", RetentionPeriod: "P3D"}},
	}
	err := m.VerifyAgainstDefinition(def)
	if err == nil || !strings.Contains(err.Error(), "retentionPeriod mismatch") {
		t.Fatalf("expected RetentionClass retentionPeriod-mismatch error, got %v", err)
	}
}
