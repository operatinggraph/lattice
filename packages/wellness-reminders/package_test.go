package wellnessreminders

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition keeps manifest.yaml and the Go
// Definition in lockstep (the loftspace-ledger / wellness-ledger precedent):
// the install reads the Definition, but the manifest is the human-facing
// declaration, and a drift between the two is a silent install hazard.
func TestPackage_ManifestMatchesDefinition(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	m, err := pkgmgr.ParseManifest(filepath.Join(wd, "manifest.yaml"))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := m.VerifyAgainstDefinition(Package); err != nil {
		t.Fatalf("manifest <-> Definition drift: %v", err)
	}
}

// TestPackage_StructurePins pins what this package declares, by count and by
// canonical name (Vertical Package Standard S6, wellness-ledger/package_test.go
// idiom). A declaration added or dropped without a deliberate edit here reds
// this test rather than reaching an install, where the same change is a
// silent capability or read-model shift.
func TestPackage_StructurePins(t *testing.T) {
	if got, want := len(Package.DDLs), 6; got != want {
		t.Errorf("DDLs: got %d, want %d", got, want)
	}
	if got, want := len(Package.Permissions), 3; got != want {
		t.Errorf("Permissions: got %d, want %d", got, want)
	}
	if got, want := len(Package.Lenses), 3; got != want {
		t.Errorf("Lenses: got %d, want %d", got, want)
	}
	if got, want := len(Package.WeaverTargets), 3; got != want {
		t.Errorf("WeaverTargets: got %d, want %d", got, want)
	}
	if got, want := len(Package.LoomPatterns), 0; got != want {
		t.Errorf("LoomPatterns: got %d, want %d", got, want)
	}
	if got, want := len(Package.OpMetas), 3; got != want {
		t.Errorf("OpMetas: got %d, want %d", got, want)
	}

	wantDDLs := []string{"bookingReminderOp", "bookingReminder", "bookingReminderNotificationOp", "bookingReminderNotification", "bookingChangeNoticeOp", "bookingChangeNotice"}
	for i, d := range Package.DDLs {
		if i < len(wantDDLs) && d.CanonicalName != wantDDLs[i] {
			t.Errorf("DDLs[%d]: got %q, want %q", i, d.CanonicalName, wantDDLs[i])
		}
	}

	wantPerms := []struct{ op, scope string }{{"RecordBookingReminder", "any"}, {"RecordBookingReminderNotification", "any"}, {"RecordBookingChangeNotice", "any"}}
	for i, want := range wantPerms {
		if i >= len(Package.Permissions) {
			break
		}
		got := Package.Permissions[i]
		if got.OperationType != want.op || got.Scope != want.scope {
			t.Errorf("Permissions[%d]: got %s/%s, want %s/%s", i, got.OperationType, got.Scope, want.op, want.scope)
		}
	}

	wantLenses := []string{"wellnessBookingReminders", "wellnessBookingChangeNotices", "pastDueBookings"}
	for i, d := range Package.Lenses {
		if i < len(wantLenses) && d.CanonicalName != wantLenses[i] {
			t.Errorf("Lenses[%d]: got %q, want %q", i, d.CanonicalName, wantLenses[i])
		}
	}

	wantTargets := []string{"wellnessBookingReminders", "wellnessBookingChangeNotices", "pastDueBookings"}
	for i, d := range Package.WeaverTargets {
		if i < len(wantTargets) && d.TargetID != wantTargets[i] {
			t.Errorf("WeaverTargets[%d]: got %q, want %q", i, d.TargetID, wantTargets[i])
		}
	}
}

// TestPastDueBookings_NoShowFeeIsATypedZero pins pastdue.go's noShowFeeCents
// param at the json:0 typed literal (internal/weaver/registry.go's Params
// grammar), not a plain string "0". GapActionSpec.Params is
// map[string]string, so an unprefixed "0" would reach wellness-domain's
// ddls.go optional_number as the Starlark string "0", which its
// `type(v) != type(0) and type(v) != type(0.0)` check rejects, returning
// None — the field never lands on .status and the sweep silently bills the
// 2500 default on every auto no-show. Decoding the literal here (mirroring
// internal/weaver/strategist.go's resolveParam, without importing that
// internal package) proves it resolves to the JSON number 0, which is what
// optional_number's type check requires.
func TestPastDueBookings_NoShowFeeIsATypedZero(t *testing.T) {
	target := pastDueBookingsTarget()
	ga, ok := target.Gaps["missing_noshow_transition"]
	if !ok {
		t.Fatalf("pastDueBookingsTarget has no missing_noshow_transition gap")
	}
	const wantParam = "json:0"
	if got := ga.Params["noShowFeeCents"]; got != wantParam {
		t.Fatalf("Params[noShowFeeCents]: got %q, want %q (a plain \"0\" reaches Starlark as a string, "+
			"which optional_number treats as absent, silently billing the 2500 default)", got, wantParam)
	}

	literal, ok := strings.CutPrefix(wantParam, "json:")
	if !ok {
		t.Fatalf("param %q does not carry the json: typed-literal prefix", wantParam)
	}
	var decoded any
	if err := json.Unmarshal([]byte(literal), &decoded); err != nil {
		t.Fatalf("literal %q is not valid JSON: %v", literal, err)
	}
	n, ok := decoded.(float64)
	if !ok || n != 0 {
		t.Fatalf("literal %q decodes to %#v, want the JSON number 0", literal, decoded)
	}
}

// TestRecordBookingChangeNotice_MarkerUpdateIsBare pins the shape of the
// .changeNotice write in recordChangeNoticeScript: the op READS the marker
// (a declared optionalRead, hydrated at step 4) and writes it as a BARE
// update — no expectedRevision. That is deliberate, not an omission: the
// Processor conditions a bare update on the hydrated revision (Contract #3
// §3.2, commit_path.go's applyHydratedRevisions) and records the condition as
// defaulted, which licenses the in-process re-hydrate + re-execute on a
// conflict. An explicit CAS is skipped by that retry path, so when both gaps
// open on one booking and Weaver dispatches both, the loser would be rejected
// outright and wait out the mark lease instead of re-executing carrying the
// winner's field. The update still carries the other kind's field forward;
// the create branch is the absent case.
func TestRecordBookingChangeNotice_MarkerUpdateIsBare(t *testing.T) {
	for _, want := range []string{
		`existing = kv.Read(booking_key + ".changeNotice")`,
		`marker_mut = {"op": "update", "key": marker_key, "document": marker_doc}`,
		`marker_mut = {"op": "create", "key": marker_key, "document": marker_doc}`,
		`marker["promotedFor"] = change_ref`,
		`marker["movedFor"] = change_ref`,
		`for field in ["promotedFor", "movedFor"]:`,
	} {
		if !strings.Contains(recordChangeNoticeScript, want) {
			t.Errorf("recordChangeNoticeScript must contain %q", want)
		}
	}
	if strings.Contains(recordChangeNoticeScript, `"expectedRevision"`) {
		t.Errorf("recordChangeNoticeScript must carry NO expectedRevision: an explicit CAS on the hydrated .changeNotice " +
			"key turns a benign two-kinds race into an unretried rejection (commit_path.go retries only §3.2-defaulted conditions)")
	}
}
