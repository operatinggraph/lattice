package descriptorform

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMountPattern pins the exact mount every cmd/{loftspace,clinic,cafe,
// wellness}-app server.go uses — inner.Handle("/shared/",
// http.StripPrefix("/shared/", http.FileServer(FS()))) — against the bug it
// fixes: FS()'s embedded tree holds "form.mjs" at its OWN root (a single-file
// //go:embed places it there directly, no "shared/" prefix), so mounting the
// bare http.FileServer(FS()) at "/shared/" with no StripPrefix looks up
// "shared/form.mjs" against a root that only has "form.mjs" and 404s on
// every request — the mount never serves the module at all. This test drives
// the mux exactly as every app registers it, catching a StripPrefix regression
// generically instead of once per app.
func TestMountPattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/shared/", http.StripPrefix("/shared/", http.FileServer(FS())))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/shared/form.mjs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /shared/form.mjs = %d, want 200 (body: %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "export function renderOpForm") {
		t.Fatalf("GET /shared/form.mjs did not serve the real module; body:\n%s", rec.Body.String())
	}

	// The unstripped shape is the regression this test exists to catch: a
	// caller that forgot StripPrefix would 404 here instead.
	rec404 := httptest.NewRecorder()
	mux.ServeHTTP(rec404, httptest.NewRequest(http.MethodGet, "/shared/nonexistent.mjs", nil))
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("GET /shared/nonexistent.mjs = %d, want 404", rec404.Code)
	}
}

// TestMountPattern_Attachments proves attachments.mjs (§22) serves through
// the same mount as form.mjs — both share one embed.FS, so a regression that
// dropped the second //go:embed pattern argument would 404 here while
// TestMountPattern still passed.
func TestMountPattern_Attachments(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/shared/", http.StripPrefix("/shared/", http.FileServer(FS())))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/shared/attachments.mjs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /shared/attachments.mjs = %d, want 200 (body: %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "export async function attachObject") {
		t.Fatalf("GET /shared/attachments.mjs did not serve the real module; body:\n%s", rec.Body.String())
	}
}

// TestMountPattern_NoStripPrefixIs404 documents (and pins) the exact failure
// this package's own FS() doc comment warns against — proof the bug is
// real, not a hypothetical.
func TestMountPattern_NoStripPrefixIs404(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/shared/", http.FileServer(FS())) // deliberately the broken mount

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/shared/form.mjs", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /shared/form.mjs through the unstripped mount = %d, want 404 "+
			"(if this now passes, FS()'s embedded layout changed and the doc comment needs a look)", rec.Code)
	}
}
