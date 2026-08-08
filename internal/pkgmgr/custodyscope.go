package pkgmgr

import "fmt"

// validateCustodyScope enforces the install-time custody rules
// (retention-class-key-custody-design.md §3.2), plus the availability gate
// rule 5 describes. Every one fails CLOSED, and they exist for the same
// reason: a package that believes it has a retention posture it does not
// actually have is worse than one that has none, because the belief is what
// stops anyone looking again.
//
// It is a pure function (no I/O) so it runs before any KV operation, the same
// doctrine as validateSensitiveClassScope, whose sibling it is.
func (def Definition) validateCustodyScope() error {
	declaredClasses := make(map[string]struct{}, len(def.RetentionClasses))
	for _, rc := range def.RetentionClasses {
		declaredClasses[rc.CanonicalName] = struct{}{}
	}

	for idx, d := range def.DDLs {
		c := d.Custody
		if c.Kind == "" && c.RetentionClass == "" {
			continue // undeclared → custody kind identity, today's model
		}

		// 1. The kind must be one this platform implements. An unrecognized
		// kind cannot be resolved at commit time, and the permissive reading
		// (treat it as identity) would silently custody a record on the very
		// subject whose erasure it was declared to survive.
		switch c.Kind {
		case CustodyKindIdentity, CustodyKindRetentionClass:
		default:
			return fmt.Errorf(
				"pkgmgr: DDL[%d] %q: Custody.Kind is %q — must be %q, %q, or empty (== %q)",
				idx, d.CanonicalName, c.Kind, CustodyKindIdentity, CustodyKindRetentionClass, CustodyKindIdentity)
		}

		// 2. Custody is meaningful only for an aspect-type DDL, mirroring
		// validateSensitiveClassScope. NOTE the empty-Class fallback is
		// opMetaClass (meta.ddl.vertexType), matching buildInstallBatch's own
		// default — defaulting to aspectType here would skip the check.
		class := d.Class
		if class == "" {
			class = opMetaClass
		}
		if class != ddlClassAspectType {
			return fmt.Errorf(
				"pkgmgr: DDL[%d] %q: Custody is declared but Class is %q — custody is meaningful only for Class %q",
				idx, d.CanonicalName, class, ddlClassAspectType)
		}

		// 3. Declaring custody for data that is never encrypted is a
		// declaration with no meaning. Ignoring it silently is how a package
		// comes to believe it has a retention posture it does not have.
		if !d.Sensitive {
			return fmt.Errorf(
				"pkgmgr: DDL[%d] %q: Custody.Kind %q is declared but Sensitive is false — custody governs a DEK, and an aspect that is never encrypted has none",
				idx, d.CanonicalName, c.Kind)
		}

		// 4. A retentionClass kind must name a class THIS package declares.
		// Cross-package references are refused: a retention class is a data
		// controller's own declaration of an obligation it answers to, not a
		// shared handle another package may bind its records to.
		if c.Kind == CustodyKindRetentionClass {
			if c.RetentionClass == "" {
				return fmt.Errorf(
					"pkgmgr: DDL[%d] %q: Custody.Kind %q requires Custody.RetentionClass naming a class this package declares",
					idx, d.CanonicalName, CustodyKindRetentionClass)
			}
			if _, ok := declaredClasses[c.RetentionClass]; !ok {
				return fmt.Errorf(
					"pkgmgr: DDL[%d] %q: Custody.RetentionClass %q is not declared by this package — a retention class may only be named by the package that declares it",
					idx, d.CanonicalName, c.RetentionClass)
			}
			// 5. Shape is valid, but the mechanism is not yet whole. A retention
			// class is a promise about a key that CAN be destroyed on the
			// class's own schedule, and the verb that destroys one —
			// ShredRetentionClassKey — does not exist (design §11 Fire 1 item
			// 3). A record custodied here would be written, and read, and then
			// held forever: precisely the outcome a retention class exists to
			// prevent, and one that reads as compliant from every surface.
			// Refusing the declaration is the fail-closed reading of a
			// half-built primitive; permitting it trades a loud install error
			// for undestroyable retained PHI. This check runs LAST so a
			// malformed declaration still gets its precise error.
			//
			// REMOVE THIS with item 3, whose green bar is a real
			// ShredRetentionClassKey against a class-custodied record.
			return fmt.Errorf(
				"pkgmgr: DDL[%d] %q: Custody.Kind %q is not installable yet — a class-custodied DEK has no destruction verb, so the retention obligation the class declares could never be discharged; this gate lifts when ShredRetentionClassKey ships",
				idx, d.CanonicalName, CustodyKindRetentionClass)
		}

		// 6. Kind identity carries no class; naming one states a custody the
		// install would not honor.
		if c.RetentionClass != "" {
			return fmt.Errorf(
				"pkgmgr: DDL[%d] %q: Custody.RetentionClass %q is set but Custody.Kind is %q — only kind %q custodies on a class",
				idx, d.CanonicalName, c.RetentionClass, CustodyKindIdentity, CustodyKindRetentionClass)
		}
	}

	return nil
}

// validateRetentionClasses enforces the declaration side: a class must be
// nameable, must state a policy this increment implements, and must not be
// declared twice. The canonicalName is what a DDL's Custody.RetentionClass
// binds to and what RetentionClassID salts the holder's NanoID from, so a
// duplicate would mint one vertex for two declared obligations.
func (def Definition) validateRetentionClasses() error {
	seen := make(map[string]struct{}, len(def.RetentionClasses))
	for idx, rc := range def.RetentionClasses {
		if rc.CanonicalName == "" {
			return fmt.Errorf("pkgmgr: RetentionClass[%d]: CanonicalName required", idx)
		}
		if _, dup := seen[rc.CanonicalName]; dup {
			return fmt.Errorf(
				"pkgmgr: RetentionClass[%d]: CanonicalName %q declared twice — the name salts the holder's NanoID, so a duplicate would custody two obligations on one key",
				idx, rc.CanonicalName)
		}
		seen[rc.CanonicalName] = struct{}{}

		if rc.Policy != RetentionPolicyEraseOnExpiry {
			return fmt.Errorf(
				"pkgmgr: RetentionClass[%d] %q: Policy is %q — %q is the only policy implemented",
				idx, rc.CanonicalName, rc.Policy, RetentionPolicyEraseOnExpiry)
		}
		if rc.RetentionPeriod == "" {
			return fmt.Errorf(
				"pkgmgr: RetentionClass[%d] %q: RetentionPeriod required — it is declarative (nothing expires a class key automatically yet), but an unstated schedule is an obligation nobody can audit",
				idx, rc.CanonicalName)
		}
		if rc.Description == "" {
			return fmt.Errorf(
				"pkgmgr: RetentionClass[%d] %q: Description required — it is where the controller records which obligation this retention answers to",
				idx, rc.CanonicalName)
		}
	}
	return nil
}
