package pkgmgr

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest mirrors the manifest.yaml schema. The on-disk file is YAML;
// the parsed struct is what `cmd/lattice-pkg` cross-validates against
// the package's Go `Definition` to catch drift.
//
// See `docs/components/_packages.md` for the canonical schema.
type Manifest struct {
	Name        string        `yaml:"name"`
	Version     string        `yaml:"version"`
	Description string        `yaml:"description,omitempty"`
	Depends     []string      `yaml:"depends,omitempty"`
	Declares    ManifestBlock `yaml:"declares"`
}

// ManifestBlock is the `declares:` sub-tree.
type ManifestBlock struct {
	DDLs        []ManifestDDL        `yaml:"ddls,omitempty"`
	Lenses      []ManifestLens       `yaml:"lenses,omitempty"`
	Permissions []ManifestPermission `yaml:"permissions,omitempty"`
}

// ManifestDDL is one DDL declaration entry. Class defaults to
// `meta.ddl.vertexType` when omitted.
type ManifestDDL struct {
	CanonicalName string `yaml:"canonicalName"`
	Class         string `yaml:"class,omitempty"`
}

// ManifestLens is one Lens declaration entry.
type ManifestLens struct {
	CanonicalName string `yaml:"canonicalName"`
	Adapter       string `yaml:"adapter,omitempty"`
	Bucket        string `yaml:"bucket,omitempty"`
	Engine        string `yaml:"engine,omitempty"`
}

// ManifestPermission is one permission declaration entry.
type ManifestPermission struct {
	OperationType string   `yaml:"operationType"`
	Scope         string   `yaml:"scope,omitempty"`
	GrantsTo      []string `yaml:"grantsTo,omitempty"`
}

// ParseManifest reads and validates a manifest.yaml file. Required
// fields:
//
//   - name (non-empty)
//   - version (non-empty)
//
// Validation only — no Core KV reads.
func ParseManifest(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: read manifest %s: %w", path, err)
	}
	return ParseManifestBytes(raw)
}

// ParseManifestBytes is the unit-testable variant of ParseManifest.
func ParseManifestBytes(raw []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("pkgmgr: parse manifest yaml: %w", err)
	}
	if strings.TrimSpace(m.Name) == "" {
		return nil, fmt.Errorf("pkgmgr: manifest.name is required")
	}
	if strings.TrimSpace(m.Version) == "" {
		return nil, fmt.Errorf("pkgmgr: manifest.version is required")
	}
	return &m, nil
}

// VerifyAgainstDefinition cross-checks a parsed manifest against the
// package's Go Definition. The two sources MUST agree on package name,
// version, declared DDL/lens/permission counts, and canonical-name
// listings. Drift surfaces as an error before any Core KV write.
func (m *Manifest) VerifyAgainstDefinition(d Definition) error {
	if m.Name != d.Name {
		return fmt.Errorf("pkgmgr: manifest.name=%q != Definition.Name=%q", m.Name, d.Name)
	}
	if m.Version != d.Version {
		return fmt.Errorf("pkgmgr: manifest.version=%q != Definition.Version=%q", m.Version, d.Version)
	}
	if got, want := len(m.Declares.DDLs), len(d.DDLs); got != want {
		return fmt.Errorf("pkgmgr: manifest declares %d DDLs but Definition has %d", got, want)
	}
	if got, want := len(m.Declares.Lenses), len(d.Lenses); got != want {
		return fmt.Errorf("pkgmgr: manifest declares %d lenses but Definition has %d", got, want)
	}
	if got, want := len(m.Declares.Permissions), len(d.Permissions); got != want {
		return fmt.Errorf("pkgmgr: manifest declares %d permissions but Definition has %d", got, want)
	}
	for i, dm := range m.Declares.DDLs {
		if dm.CanonicalName != d.DDLs[i].CanonicalName {
			return fmt.Errorf("pkgmgr: DDL[%d] canonicalName mismatch: manifest=%q definition=%q",
				i, dm.CanonicalName, d.DDLs[i].CanonicalName)
		}
	}
	for i, lm := range m.Declares.Lenses {
		if lm.CanonicalName != d.Lenses[i].CanonicalName {
			return fmt.Errorf("pkgmgr: Lens[%d] canonicalName mismatch: manifest=%q definition=%q",
				i, lm.CanonicalName, d.Lenses[i].CanonicalName)
		}
	}
	for i, pm := range m.Declares.Permissions {
		if pm.OperationType != d.Permissions[i].OperationType {
			return fmt.Errorf("pkgmgr: Permission[%d] operationType mismatch: manifest=%q definition=%q",
				i, pm.OperationType, d.Permissions[i].OperationType)
		}
	}
	return nil
}
