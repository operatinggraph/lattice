package pkgmgr

import "fmt"

// ddlClassAspectType is the DDL Class value a sensitive aspect DDL declares.
// Mirrors buildInstallBatch's own default (build.go): an empty Class defaults
// to vertexType, never aspectType, so only an explicit aspectType DDL can ever
// be legitimately sensitive.
const ddlClassAspectType = "meta.ddl.aspectType"

// validateSensitiveClassScope rejects a DDLSpec that declares Sensitive: true
// on anything other than an aspectType DDL. Sensitive is meaningful only for
// an aspect (its doc comment says so), but nothing enforced that: the Vault
// crypto-shredding boundary (step 6.5 encrypt, decrypt-on-read) resolves and
// acts on sensitivity only for aspect mutations/documents, and kv.Links never
// applies a read disposition to a link's data at all — so a Sensitive: true
// link (or event, or vertexType) DDL would install successfully yet never be
// encrypted, never be write-scope-rejected, and never be decrypt-protected on
// read, a silent gap rather than a fail-closed rejection. Install-time
// validation is a pure function (no I/O) so it runs before any KV operation,
// same doctrine as every other package-data validator in this file.
func (def Definition) validateSensitiveClassScope() error {
	for idx, d := range def.DDLs {
		if !d.Sensitive {
			continue
		}
		class := d.Class
		if class == "" {
			class = opMetaClass
		}
		if class != ddlClassAspectType {
			return fmt.Errorf(
				"pkgmgr: DDL[%d] %q: Sensitive is true but Class is %q — sensitive is meaningful only for Class %q",
				idx, d.CanonicalName, class, ddlClassAspectType)
		}
	}
	return nil
}
