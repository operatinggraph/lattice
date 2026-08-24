package pkgmgr

// BuildInstallBatchForTest exposes the internal install-batch builder to the
// external pkgmgr_test package so a test can round-trip the emitted
// orchestration bodies through the engine parse structs (weaver.Target /
// loom.Pattern) — the regression that proves the seam emits exactly what the
// engines load, with no engine change. Test-only; not part of the public API.
func BuildInstallBatchForTest(def Definition) ([]InstallMutationForTest, []string, error) {
	return BuildInstallBatchWithSubtypesForTest(def, nil)
}

// BuildInstallBatchWithSubtypesForTest is BuildInstallBatchForTest plus an
// explicit DDL-index -> abstract-meta-vertex-NanoID map, so a test can pin
// the `subtypeOf` link emission (build.go) without standing up a live
// Installer.resolveTaxonomy resolution (installer.go, which needs a
// substrate connection). Test-only; not part of the public API.
func BuildInstallBatchWithSubtypesForTest(def Definition, subtypeAbstractIDs map[int]string) ([]InstallMutationForTest, []string, error) {
	inst := &Installer{}
	pkgKey := PackageVertexPrefix + entityNanoID(def.Name, "package")

	ddlIDs := make([]string, len(def.DDLs))
	lensIDs := make([]string, len(def.Lenses))
	permIDs := make([]string, len(def.Permissions))
	roleIDs := make([]string, len(def.Roles))
	weaverTargetIDs := make([]string, len(def.WeaverTargets))
	loomPatternIDs := make([]string, len(def.LoomPatterns))
	opMetaIDs := make([]string, len(def.OpMetas))
	for idx, d := range def.DDLs {
		ddlIDs[idx] = entityNanoID(def.Name, "ddl:"+d.CanonicalName)
	}
	for idx, l := range def.Lenses {
		lensIDs[idx] = entityNanoID(def.Name, "lens:"+l.CanonicalName)
	}
	for idx, p := range def.Permissions {
		permIDs[idx] = entityNanoID(def.Name, permTag(p.OperationType, p.Scope))
	}
	for idx, r := range def.Roles {
		roleIDs[idx] = entityNanoID(def.Name, "role:"+r.CanonicalName)
	}
	for idx, t := range def.WeaverTargets {
		weaverTargetIDs[idx] = entityNanoID(def.Name, "weaverTarget:"+t.TargetID)
	}
	for idx, p := range def.LoomPatterns {
		loomPatternIDs[idx] = entityNanoID(def.Name, "loomPattern:"+p.PatternID)
	}
	for idx, o := range def.OpMetas {
		opMetaIDs[idx] = entityNanoID(def.Name, "opMeta:"+o.OperationType)
	}
	paneIDs := make([]string, len(def.Panes))
	for idx, p := range def.Panes {
		paneIDs[idx] = entityNanoID(def.Name, "pane:"+p.CanonicalName)
	}
	if inst.RoleIDs == nil {
		inst.RoleIDs = map[string]string{}
	}
	for idx, r := range def.Roles {
		inst.RoleIDs[r.CanonicalName] = roleIDs[idx]
	}
	def = inst.resolvePaneRoles(def)
	retentionClassIDs := make([]string, len(def.RetentionClasses))
	for idx, rc := range def.RetentionClasses {
		retentionClassIDs[idx] = RetentionClassID(def.Name, rc.CanonicalName)
	}

	ops, declared, err := inst.buildInstallBatch(def, pkgKey, ddlIDs, lensIDs, permIDs, roleIDs,
		weaverTargetIDs, loomPatternIDs, opMetaIDs, paneIDs, retentionClassIDs, subtypeAbstractIDs)
	if err != nil {
		return nil, nil, err
	}
	out := make([]InstallMutationForTest, len(ops))
	for idx, op := range ops {
		out[idx] = InstallMutationForTest(op)
	}
	return out, declared, nil
}

// InstallMutationForTest mirrors the internal installMutation so the external
// test package can read emitted keys/documents.
type InstallMutationForTest struct {
	Op               string
	Key              string
	Document         map[string]any
	ExpectedRevision *uint64
}

// EntityNanoIDForTest exposes the installer's version-independent entity
// NanoID minting so tests can recompute the id a given entity will be keyed
// under (Contract #8 §8.1 — derived from package name + entity tag, no
// version).
func EntityNanoIDForTest(name, tag string) string {
	return entityNanoID(name, tag)
}

// PermTagForTest exposes the version-independent permission identity tag so
// tests can recompute a permission's entity key from its operationType+scope.
func PermTagForTest(operationType, scope string) string {
	return permTag(operationType, scope)
}

// LensNeedsCapCheckForTest exposes the install-time label-cap gate's only
// precondition — exhaustive, and carrying at least one `*` sigil — to the
// external pkgmgr_test package. It exists so a test can pin the exemption of
// the SHIPPED sigil-bearing lens against that package's REAL source, which the
// internal test package cannot import: packages/service-location imports
// pkgmgr, so `package pkgmgr` importing it back would cycle. Test-only; not
// part of the public API.
func LensNeedsCapCheckForTest(facts SpecLabels) bool { return lensNeedsCapCheck(facts) }

// ValidateWeaverTargetsForTest exposes the §10.8 install-time weaver-target
// validations — including the Contract #10 §10.3 companion-pair gate — to the
// external pkgmgr_test package. It exists so a test can run the gate against
// every SHIPPED package's real Definition, which the internal test package
// cannot reach: every package under packages/ imports pkgmgr, so
// `package pkgmgr` importing pkgregistry back would cycle. Test-only; not part
// of the public API.
func ValidateWeaverTargetsForTest(def Definition) error {
	// Production never validates a raw Definition: Install/Upgrade/Apply expand
	// the read-grant walks first and validate the expansion (installer.go,
	// upgrade.go, validateAll). Expanding here keeps a corpus sweep measuring
	// what an install would actually see, rather than a shape that happens to
	// be equivalent today.
	expanded, err := def.ExpandReadGrantWalks()
	if err != nil {
		return err
	}
	return expanded.validateWeaverTargets()
}
