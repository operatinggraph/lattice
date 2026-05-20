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
		Name:    "x",
		Version: "1.0",
		DDLs:    []DDLSpec{{CanonicalName: "A"}},
		Lenses:  []LensSpec{{CanonicalName: "L"}},
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
