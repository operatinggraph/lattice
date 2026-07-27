package clinicreminders

import (
	"strings"
	"testing"
)

// TestWorkplaceAnchor_VisitSeriesUsesComprehension mirrors clinic-domain's
// TestWorkplaceAnchor_AppointmentsUseComprehension: the workplace token must
// be a pattern comprehension, because withProvider is OPTIONAL here and a
// provider-less series would otherwise put a NULL element in authz_anchors —
// which the Protected adapter rejects, failing the row's upsert and hiding
// the series from its own PATIENT.
func TestWorkplaceAnchor_VisitSeriesUsesComprehension(t *testing.T) {
	spec := visitSeriesReadSpec

	if !strings.Contains(spec, "[(pr)-[:practicesAt]->(b:building) | nanoIdFromKey(b.key)]") {
		t.Fatal("the workplace anchor must be a pattern comprehension over the provider's building")
	}
	if !strings.Contains(spec, "[nanoIdFromKey(p.key)] +") {
		t.Error("the patient anchor must remain the first, unconditional element")
	}
	if strings.Contains(spec, "[nanoIdFromKey(p.key), nanoIdFromKey(") {
		t.Error("two-element array literal reintroduces the null-element hazard")
	}
}
