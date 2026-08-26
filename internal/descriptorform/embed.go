// Package descriptorform serves the shared op-form renderer
// (form.mjs) that every staff vertical app mounts at /shared/ beside its own
// //go:embed web FileServer (staff-descriptor-rendering-design.md §13). One
// implementation of the op-catalog descriptor vocabulary — schema-to-field-
// kind detection, template substitution, and authContext assembly — so a
// staff app's server stops re-declaring op shapes the owning package already
// declares. attachments.mjs (§22) is the same idea for the AttachObject/
// DetachObject client ceremony, which stays hand-built by design (§2.3) but
// need not be re-derived per app.
package descriptorform

import (
	"embed"
	"net/http"
)

//go:embed form.mjs attachments.mjs
var formFS embed.FS

// FS serves the module at its own root — form.mjs sits directly under this
// package directory, so a single-file embed already places it at "form.mjs"
// with no fs.Sub needed. The returned tree has no "/shared/" prefix of its
// own, so a caller mounting it at that path MUST strip the prefix before it
// reaches the file server, e.g.
// `inner.Handle("/shared/", http.StripPrefix("/shared/", http.FileServer(descriptorform.FS())))`
// — omitting StripPrefix looks up "shared/form.mjs" against a root that only
// holds "form.mjs", and 404s on every request.
func FS() http.FileSystem {
	return http.FS(formFS)
}
