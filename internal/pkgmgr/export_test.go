package pkgmgr

// BuildInstallBatchForTest exposes the internal install-batch builder to the
// external pkgmgr_test package so a test can round-trip the emitted
// orchestration bodies through the engine parse structs (weaver.Target /
// loom.Pattern) — the regression that proves the seam emits exactly what the
// engines load, with no engine change. Test-only; not part of the public API.
func BuildInstallBatchForTest(def Definition) ([]InstallMutationForTest, []string, error) {
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

	ops, declared, err := inst.buildInstallBatch(def, pkgKey, ddlIDs, lensIDs, permIDs, roleIDs,
		weaverTargetIDs, loomPatternIDs, opMetaIDs)
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
