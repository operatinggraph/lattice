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
	DDLs             []ManifestDDL            `yaml:"ddls,omitempty"`
	Lenses           []ManifestLens           `yaml:"lenses,omitempty"`
	Permissions      []ManifestPermission     `yaml:"permissions,omitempty"`
	WeaverTargets    []ManifestWeaverTarget   `yaml:"weaverTargets,omitempty"`
	LoomPatterns     []ManifestLoomPattern    `yaml:"loomPatterns,omitempty"`
	OpMetas          []ManifestOpMeta         `yaml:"opMetas,omitempty"`
	Panes            []ManifestPane           `yaml:"panes,omitempty"`
	RetentionClasses []ManifestRetentionClass `yaml:"retentionClasses,omitempty"`
}

// ManifestDDL is one DDL declaration entry. Class defaults to
// `meta.ddl.vertexType` when omitted.
//
// Abstract and SubtypeOf carry the taxonomy declaration
// (dynamic-type-taxonomy-design.md §3.5). Without them a package declaring an
// abstract type and its concrete leaves lists several indistinguishable
// vertexType rows, and the manifest — whose whole job is to be the reviewable
// statement of what a package declares — cannot say which is the parent, nor
// catch a Definition that silently flips one.
type ManifestDDL struct {
	CanonicalName string `yaml:"canonicalName"`
	Class         string `yaml:"class,omitempty"`
	// Abstract mirrors DDLSpec.Abstract: this type names no instance and is a
	// taxonomy parent only.
	Abstract bool `yaml:"abstract,omitempty"`
	// SubtypeOf mirrors DDLSpec.SubtypeOfRef: the canonicalName of the type
	// this one is a subtypeOf. Empty for a type that declares no parent.
	SubtypeOf string `yaml:"subtypeOf,omitempty"`
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

// ManifestWeaverTarget is one weaver-target declaration entry. The identity
// field VerifyAgainstDefinition cross-checks is `targetId`.
type ManifestWeaverTarget struct {
	TargetID string `yaml:"targetId"`
	LensRef  string `yaml:"lensRef,omitempty"`
}

// ManifestLoomPattern is one loom-pattern declaration entry. The identity
// field VerifyAgainstDefinition cross-checks is `patternId`.
type ManifestLoomPattern struct {
	PatternID   string `yaml:"patternId"`
	SubjectType string `yaml:"subjectType,omitempty"`
}

// ManifestOpMeta is one op-meta declaration entry. The identity field
// VerifyAgainstDefinition cross-checks is `operationType`.
type ManifestOpMeta struct {
	OperationType string `yaml:"operationType"`
}

// ManifestPane is one pane declaration entry. The identity field
// VerifyAgainstDefinition cross-checks is `paneId`.
type ManifestPane struct {
	PaneID string `yaml:"paneId"`
}

// ManifestRetentionClass is one retention-class declaration entry. The
// identity field VerifyAgainstDefinition cross-checks is `canonicalName`.
//
// Policy and RetentionPeriod carry the data controller's actual obligation
// (retention-class-key-custody-design.md §3.1), not just which class exists:
// without them an author can change a retention period from years to days,
// or flip the policy, with a manifest diff that shows nothing — mirroring
// why ManifestDDL's Abstract/SubtypeOf are compared alongside CanonicalName
// rather than treated as decoration. A retention class's whole purpose is
// being the reviewable statement of that obligation; identity alone is not
// enough to review a change to it.
type ManifestRetentionClass struct {
	CanonicalName   string `yaml:"canonicalName"`
	Policy          string `yaml:"policy,omitempty"`
	RetentionPeriod string `yaml:"retentionPeriod,omitempty"`
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
	// Compare against the COMPOSED Definition: a generated cap-read producer is
	// a real declared lens the installer emits a meta-vertex for, so the
	// manifest lists it like any other. Lens comparison below is index-wise,
	// which is why generated producers are appended in ReadGrantDomains order
	// and the manifest's lens list must follow the same order.
	d, err := d.ExpandReadGrantWalks()
	if err != nil {
		return err
	}
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
	if got, want := len(m.Declares.WeaverTargets), len(d.WeaverTargets); got != want {
		return fmt.Errorf("pkgmgr: manifest declares %d weaverTargets but Definition has %d", got, want)
	}
	if got, want := len(m.Declares.LoomPatterns), len(d.LoomPatterns); got != want {
		return fmt.Errorf("pkgmgr: manifest declares %d loomPatterns but Definition has %d", got, want)
	}
	if got, want := len(m.Declares.OpMetas), len(d.OpMetas); got != want {
		return fmt.Errorf("pkgmgr: manifest declares %d opMetas but Definition has %d", got, want)
	}
	if got, want := len(m.Declares.Panes), len(d.Panes); got != want {
		return fmt.Errorf("pkgmgr: manifest declares %d panes but Definition has %d", got, want)
	}
	if got, want := len(m.Declares.RetentionClasses), len(d.RetentionClasses); got != want {
		return fmt.Errorf("pkgmgr: manifest declares %d retentionClasses but Definition has %d", got, want)
	}
	for i, dm := range m.Declares.DDLs {
		if dm.CanonicalName != d.DDLs[i].CanonicalName {
			return fmt.Errorf("pkgmgr: DDL[%d] canonicalName mismatch: manifest=%q definition=%q",
				i, dm.CanonicalName, d.DDLs[i].CanonicalName)
		}
		// The taxonomy fields are part of a DDL's identity, not decoration: a
		// type flipping between abstract and concrete changes whether it may
		// carry a script, whether any instance may key or class under it, and
		// what a `*` lens label expands to. A manifest that cannot disagree
		// with the Definition about them cannot review them.
		if dm.Abstract != d.DDLs[i].Abstract {
			return fmt.Errorf("pkgmgr: DDL[%d] (%s) abstract mismatch: manifest=%v definition=%v",
				i, dm.CanonicalName, dm.Abstract, d.DDLs[i].Abstract)
		}
		if dm.SubtypeOf != d.DDLs[i].SubtypeOfRef {
			return fmt.Errorf("pkgmgr: DDL[%d] (%s) subtypeOf mismatch: manifest=%q definition=%q",
				i, dm.CanonicalName, dm.SubtypeOf, d.DDLs[i].SubtypeOfRef)
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
		// A permission's identity is its (operationType, scope) pair
		// (Contract #8 §8.1 permTag), so a manifest agreeing on the op but
		// naming the other scope describes a DIFFERENT permission vertex than
		// the one the install writes. An omitted manifest scope is the
		// unstated default `any`, matching ParseManifest's own leniency.
		if ms := pm.Scope; ms != "" && ms != d.Permissions[i].Scope {
			return fmt.Errorf("pkgmgr: Permission[%d] (%s) scope mismatch: manifest=%q definition=%q",
				i, pm.OperationType, ms, d.Permissions[i].Scope)
		}
		// Cross-check GrantsTo lists so a manifest that drifts from the Go
		// Definition is caught before any Core KV write (the install uses
		// the Definition's GrantsTo, not the manifest's).
		if err := crossCheckGrantsTo(i, pm.GrantsTo, d.Permissions[i].GrantsTo); err != nil {
			return err
		}
	}
	for i, tm := range m.Declares.WeaverTargets {
		if tm.TargetID != d.WeaverTargets[i].TargetID {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] targetId mismatch: manifest=%q definition=%q",
				i, tm.TargetID, d.WeaverTargets[i].TargetID)
		}
	}
	for i, pm := range m.Declares.LoomPatterns {
		if pm.PatternID != d.LoomPatterns[i].PatternID {
			return fmt.Errorf("pkgmgr: LoomPattern[%d] patternId mismatch: manifest=%q definition=%q",
				i, pm.PatternID, d.LoomPatterns[i].PatternID)
		}
	}
	for i, om := range m.Declares.OpMetas {
		if om.OperationType != d.OpMetas[i].OperationType {
			return fmt.Errorf("pkgmgr: OpMeta[%d] operationType mismatch: manifest=%q definition=%q",
				i, om.OperationType, d.OpMetas[i].OperationType)
		}
	}
	for i, pm := range m.Declares.Panes {
		if pm.PaneID != d.Panes[i].CanonicalName {
			return fmt.Errorf("pkgmgr: Pane[%d] paneId mismatch: manifest=%q definition=%q",
				i, pm.PaneID, d.Panes[i].CanonicalName)
		}
	}
	for i, rm := range m.Declares.RetentionClasses {
		if rm.CanonicalName != d.RetentionClasses[i].CanonicalName {
			return fmt.Errorf("pkgmgr: RetentionClass[%d] canonicalName mismatch: manifest=%q definition=%q",
				i, rm.CanonicalName, d.RetentionClasses[i].CanonicalName)
		}
		// Policy/RetentionPeriod are the actual data-controller obligation, not
		// decoration on the class's identity — a change to either must show up
		// as a manifest diff (see ManifestRetentionClass's doc comment).
		if rm.Policy != d.RetentionClasses[i].Policy {
			return fmt.Errorf("pkgmgr: RetentionClass[%d] (%s) policy mismatch: manifest=%q definition=%q",
				i, rm.CanonicalName, rm.Policy, d.RetentionClasses[i].Policy)
		}
		if rm.RetentionPeriod != d.RetentionClasses[i].RetentionPeriod {
			return fmt.Errorf("pkgmgr: RetentionClass[%d] (%s) retentionPeriod mismatch: manifest=%q definition=%q",
				i, rm.CanonicalName, rm.RetentionPeriod, d.RetentionClasses[i].RetentionPeriod)
		}
	}
	return nil
}

// crossCheckGrantsTo compares the manifest and Definition GrantsTo lists for
// one permission entry. Ordering is irrelevant; set-equality is checked.
func crossCheckGrantsTo(idx int, manifestGrants, defGrants []string) error {
	if len(manifestGrants) != len(defGrants) {
		return fmt.Errorf("pkgmgr: Permission[%d] grantsTo count mismatch: manifest=%d definition=%d",
			idx, len(manifestGrants), len(defGrants))
	}
	// Build a set from the definition's grants.
	defSet := make(map[string]struct{}, len(defGrants))
	for _, g := range defGrants {
		defSet[g] = struct{}{}
	}
	for _, g := range manifestGrants {
		if _, ok := defSet[g]; !ok {
			return fmt.Errorf("pkgmgr: Permission[%d] grantsTo: manifest has %q but Definition does not", idx, g)
		}
	}
	return nil
}
