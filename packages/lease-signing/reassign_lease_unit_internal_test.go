package leasesigning

import (
	"strings"
	"testing"
)

// TestReassignLeaseUnit_TombstonesWithExpectedRevision text-pins the
// revision-pinned tombstone (mirrors dispatch_reads_guard_test.go's opBranch
// idiom for a property no pipeline observation surfaces — the OCC
// expectedRevision field never appears in the committed document). Two
// concurrent re-points to different units must not both land: the second must
// RevisionConflicts rather than leaving the leaseapp with two live
// appliesToUnit links, which is exactly what tombstoning the OLD link via
// make_link_tombstone_occ (not the unconditioned make_link_tombstone)
// guarantees.
func TestReassignLeaseUnit_TombstonesWithExpectedRevision(t *testing.T) {
	branch := opBranch(t, leaseAppDDLScript, "ReassignLeaseUnit")
	if !strings.Contains(branch, "make_link_tombstone_occ(current.key") {
		t.Fatalf("ReassignLeaseUnit must tombstone the OLD appliesToUnit link via make_link_tombstone_occ (revision-pinned), got branch:\n%s", branch)
	}
	if strings.Contains(branch, "make_link_tombstone(current.key") {
		t.Fatalf("ReassignLeaseUnit must not use the unconditioned make_link_tombstone on the old link")
	}
}
