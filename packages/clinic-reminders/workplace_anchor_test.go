package clinicreminders

import (
	"strings"
	"testing"
)

// TestWorkplaceAnchor_VisitSeriesUsesComprehension mirrors clinic-domain's
// TestWorkplaceAnchor_AppointmentsUseComprehension: BOTH workplace arms must be
// pattern comprehensions, because both are OPTIONAL here — a provider-less (or
// tombstoned-provider) series, and a site-less one — and a bare element off
// either would put a NULL element in authz_anchors, which the Protected adapter
// rejects, failing the row's upsert and hiding the series from its own PATIENT.
func TestWorkplaceAnchor_VisitSeriesUsesComprehension(t *testing.T) {
	spec := visitSeriesReadSpec

	if !strings.Contains(spec, "[(pr)-[:practicesAt]->(b:building) | nanoIdFromKey(b.key)]") {
		t.Fatal("the workplace anchor must be a pattern comprehension over the provider's building")
	}
	if !strings.Contains(spec, "[(s)-[:atSite]->(sb:building) | nanoIdFromKey(sb.key)]") {
		t.Fatal("the tombstoned-provider fallback must be a pattern comprehension over the series' own atSite building")
	}
	if !strings.Contains(spec, "CASE WHEN (pr)-[:practicesAt]->(pb:building)") {
		t.Fatal("the two arms must be selected by a CASE on the practicesAt walk (enforce_workplace's own order), " +
			"never unioned — a union double-counts the site of a live provider that practises there")
	}
	if !strings.Contains(spec, "[nanoIdFromKey(p.key)]\n    + (CASE") {
		t.Error("the patient anchor must remain the first, unconditional element")
	}
	if strings.Contains(spec, "[nanoIdFromKey(p.key), nanoIdFromKey(") {
		t.Error("two-element array literal reintroduces the null-element hazard")
	}
}
